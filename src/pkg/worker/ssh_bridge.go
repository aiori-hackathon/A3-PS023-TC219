package worker

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// SSHBridge provides an SSH server endpoint that bridges incoming SSH sessions
// to a VM's Unix domain serial console socket.
type SSHBridge struct {
	listener  net.Listener
	sockPath  string
	port      int
	hostKey   ssh.Signer
	stopCh    chan struct{}
	closeOnce sync.Once
}

func generateHostKey() (ssh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(key)
}

// StartSSHBridge launches a local SSH server listening on 0.0.0.0:<port>
// and bridges interactive SSH sessions to the VM's serial socket.
func StartSSHBridge(sockPath string, port int, signer ssh.Signer) (*SSHBridge, error) {
	if signer == nil {
		var err error
		signer, err = generateHostKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate host key: %w", err)
		}
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("failed to listen on SSH port %d: %w", port, err)
	}

	b := &SSHBridge{
		listener: listener,
		sockPath: sockPath,
		port:     port,
		hostKey:  signer,
		stopCh:   make(chan struct{}),
	}

	go b.acceptLoop()
	return b, nil
}

// Port returns the listening TCP port.
func (b *SSHBridge) Port() int {
	return b.port
}

// Close stops the bridge listener.
func (b *SSHBridge) Close() {
	b.closeOnce.Do(func() {
		close(b.stopCh)
		if b.listener != nil {
			_ = b.listener.Close()
		}
	})
}

func (b *SSHBridge) acceptLoop() {
	config := &ssh.ServerConfig{
		NoClientAuth: true,
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	config.AddHostKey(b.hostKey)

	for {
		nConn, err := b.listener.Accept()
		if err != nil {
			select {
			case <-b.stopCh:
				return
			default:
				return
			}
		}

		go b.handleConn(nConn, config)
	}
}

func (b *SSHBridge) handleConn(nConn net.Conn, config *ssh.ServerConfig) {
	defer nConn.Close()

	sConn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return
	}
	defer sConn.Close()

	// Discard global out-of-band requests
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}

		go b.handleSession(channel, requests)
	}
}

func (b *SSHBridge) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "pty-req", "window-change", "env":
			_ = req.Reply(true, nil)
		case "shell", "exec":
			_ = req.Reply(true, nil)
			go ssh.DiscardRequests(requests)
			b.bridgeToSerial(channel)
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (b *SSHBridge) bridgeToSerial(channel ssh.Channel) {
	sockConn, err := net.Dial("unix", b.sockPath)
	if err != nil {
		_, _ = channel.Write([]byte(fmt.Sprintf("\r\n[SSH Bridge] Error connecting to VM serial console: %v\r\n", err)))
		return
	}
	defer sockConn.Close()

	// Send an initial newline to trigger prompt display if guest is waiting at prompt
	_, _ = sockConn.Write([]byte("\r\n"))

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(channel, sockConn)
		_ = channel.Close()
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(sockConn, channel)
		_ = sockConn.Close()
		done <- struct{}{}
	}()

	<-done
}
