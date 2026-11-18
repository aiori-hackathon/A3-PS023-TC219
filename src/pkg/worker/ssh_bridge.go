package worker

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

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

func loadOrGenerateHostKey(keyPath string) (ssh.Signer, error) {
	if keyPath != "" {
		if data, err := os.ReadFile(keyPath); err == nil {
			signer, err := ssh.ParsePrivateKey(data)
			if err == nil {
				return signer, nil
			}
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	if keyPath != "" {
		keyBytes := x509.MarshalPKCS1PrivateKey(key)
		pemBlock := &pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: keyBytes,
		}
		_ = os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600)
	}

	return ssh.NewSignerFromKey(key)
}

func generateHostKey() (ssh.Signer, error) {
	return loadOrGenerateHostKey("")
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

		go b.handleSession(sConn, channel, requests)
	}
}

func (b *SSHBridge) handleSession(sConn *ssh.ServerConn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() {
		_ = channel.Close()
		_ = sConn.Close()
	}()

	for req := range requests {
		switch req.Type {
		case "pty-req", "window-change", "env":
			_ = req.Reply(true, nil)
		case "shell", "exec":
			_ = req.Reply(true, nil)
			go ssh.DiscardRequests(requests)
			b.bridgeToSerial(sConn, channel)
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (b *SSHBridge) bridgeToSerial(sConn *ssh.ServerConn, channel ssh.Channel) {
	sockConn, err := net.Dial("unix", b.sockPath)
	if err != nil {
		_, _ = channel.Write([]byte(fmt.Sprintf("\r\n[SSH Bridge] Error connecting to VM serial console: %v\r\n", err)))
		return
	}
	defer sockConn.Close()

	banner := "\r\n" +
		"==========================================================\r\n" +
		" Welcome to MINIX 3 / Microkernel Guest VM\r\n" +
		" Node: Cloud Hypervisor on Linux KVM\r\n" +
		" Interactive shell ready.\r\n" +
		" Disconnect: press Ctrl+D\r\n" +
		"==========================================================\r\n\r\n"
	_, _ = channel.Write([]byte(banner))

	// Send an initial newline to trigger prompt display if guest is waiting at prompt
	_, _ = sockConn.Write([]byte("\r\n"))

	done := make(chan struct{}, 2)

	go func() {
		_, _ = io.Copy(channel, sockConn)
		_ = channel.Close()
		_ = sConn.Close()
		done <- struct{}{}
	}()

	go func() {
		defer func() {
			_ = sockConn.Close()
			_ = channel.Close()
			_ = sConn.Close()
			done <- struct{}{}
		}()

		buf := make([]byte, 1024)

		for {
			n, err := channel.Read(buf)
			if n > 0 {
				data := buf[:n]
				shouldExit := false

				for i := 0; i < len(data); i++ {
					if data[i] == 0x04 { // Ctrl+D (EOT)
						shouldExit = true
						break
					}
				}

				// Forward all typed bytes to the guest VM serial socket
				if _, writeErr := sockConn.Write(data); writeErr != nil {
					return
				}

				if shouldExit {
					time.Sleep(50 * time.Millisecond)
					_, _ = channel.Write([]byte("\r\n[SSH Bridge] Connection closed.\r\n"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					_ = channel.Close()
					_ = sConn.Close()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	<-done
}
