#!/bin/bash
# ==============================================================================
# Automated AWS CLI Deployment Script for Cloud Hypervisor VM Controller
#
# Usage:
#   ./deploy/deploy-aws.sh
# Or with environment overrides:
#   KEY_NAME="my-key" S3_BUCKET="my-os-bucket" ./deploy/deploy-aws.sh
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_FILE="${SCRIPT_DIR}/cloudformation.yaml"
STACK_NAME="${STACK_NAME:-vm-controller-stack}"
INSTANCE_TYPE="${INSTANCE_TYPE:-c7i-flex.2xlarge}"

echo "=================================================================="
echo " Cloud Hypervisor VM Controller - AWS Deployment"
echo " Target Instance Type: ${INSTANCE_TYPE}"
echo "=================================================================="

# 1. Verify AWS CLI and Credentials
if ! command -v aws &>/dev/null; then
    echo "Error: 'aws' CLI is not installed."
    echo "Install it via: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
    exit 1
fi

echo "--> Checking AWS authentication..."
IDENTITY=$(aws sts get-caller-identity --output json 2>/dev/null) || {
    echo "Error: Unable to authenticate with AWS. Please run 'aws configure' or set AWS credentials."
    exit 1
}
AWS_ACCOUNT=$(echo "$IDENTITY" | grep -o '"Account": "[^"]*' | cut -d'"' -f4)
AWS_ARN=$(echo "$IDENTITY" | grep -o '"Arn": "[^"]*' | cut -d'"' -f4)
REGION="${AWS_REGION:-$(aws configure get region || echo "us-east-1")}"
echo "✔ Connected to AWS Account ${AWS_ACCOUNT} in ${REGION} as ${AWS_ARN}"

# 2. Select / Auto-detect VPC
VPC_ID="${VPC_ID:-}"
if [ -z "$VPC_ID" ]; then
    echo "--> Auto-detecting default VPC..."
    VPC_ID=$(aws ec2 describe-vpcs --filters "Name=isDefault,Values=true" --query "Vpcs[0].VpcId" --output text --region "$REGION" 2>/dev/null || true)
    if [ "$VPC_ID" == "None" ] || [ -z "$VPC_ID" ]; then
        VPC_ID=$(aws ec2 describe-vpcs --query "Vpcs[0].VpcId" --output text --region "$REGION" 2>/dev/null || true)
    fi
fi
if [ -z "$VPC_ID" ] || [ "$VPC_ID" == "None" ]; then
    echo "Error: No VPC found in region ${REGION}. Specify VPC_ID=<vpc-xxxx>."
    exit 1
fi
echo "✔ Using VPC: ${VPC_ID}"

# 3. Select / Auto-detect Public Subnet
SUBNET_ID="${SUBNET_ID:-}"
if [ -z "$SUBNET_ID" ]; then
    echo "--> Auto-detecting public subnet in VPC ${VPC_ID}..."
    SUBNET_ID=$(aws ec2 describe-subnets --filters "Name=vpc-id,Values=${VPC_ID}" --query "Subnets[0].SubnetId" --output text --region "$REGION" 2>/dev/null || true)
fi
if [ -z "$SUBNET_ID" ] || [ "$SUBNET_ID" == "None" ]; then
    echo "Error: No subnet found in VPC ${VPC_ID}. Specify SUBNET_ID=<subnet-xxxx>."
    exit 1
fi
echo "✔ Using Subnet: ${SUBNET_ID}"

# 4. Select / Auto-detect Key Pair
KEY_NAME="${KEY_NAME:-}"
if [ -z "$KEY_NAME" ]; then
    echo "--> Checking available EC2 KeyPairs..."
    KEY_NAME=$(aws ec2 describe-key-pairs --query "KeyPairs[0].KeyName" --output text --region "$REGION" 2>/dev/null || true)
fi
if [ -z "$KEY_NAME" ] || [ "$KEY_NAME" == "None" ]; then
    echo "Error: No EC2 KeyPair found in region ${REGION}."
    echo "Please create a KeyPair: aws ec2 create-key-pair --key-name vm-key --query 'KeyMaterial' --output text > vm-key.pem"
    exit 1
fi
echo "✔ Using Key Pair: ${KEY_NAME}"

# 5. S3 Bucket & IAM Role
S3_BUCKET="${S3_BUCKET:-}"
if [ -z "$S3_BUCKET" ]; then
    read -r -p "Enter S3 bucket name containing your Linux ISOs (leave blank if none): " S3_INPUT || true
    S3_BUCKET="${S3_INPUT:-}"
fi
IAM_ROLE="${IAM_ROLE:-}"

# 6. Deploy via CloudFormation
echo ""
echo "--> Deploying CloudFormation Stack: ${STACK_NAME} in ${REGION}..."
PARAMS="ParameterKey=InstanceType,ParameterValue=${INSTANCE_TYPE} \
ParameterKey=KeyName,ParameterValue=${KEY_NAME} \
ParameterKey=VpcId,ParameterValue=${VPC_ID} \
ParameterKey=SubnetId,ParameterValue=${SUBNET_ID}"

if [ -n "$IAM_ROLE" ]; then
    PARAMS="$PARAMS ParameterKey=IamInstanceProfile,ParameterValue=${IAM_ROLE}"
fi
if [ -n "$S3_BUCKET" ]; then
    PARAMS="$PARAMS ParameterKey=S3BucketName,ParameterValue=${S3_BUCKET}"
fi

aws cloudformation create-stack \
    --stack-name "${STACK_NAME}" \
    --template-body "file://${TEMPLATE_FILE}" \
    --parameters ${PARAMS} \
    --capabilities CAPABILITY_IAM \
    --region "${REGION}"

echo "Stack creation initiated. Waiting for stack resources to complete (typically ~2 minutes)..."
aws cloudformation wait stack-create-complete --stack-name "${STACK_NAME}" --region "${REGION}"

echo ""
echo "=================================================================="
echo " Deployment Complete! Stack Outputs:"
echo "=================================================================="
aws cloudformation describe-stacks \
    --stack-name "${STACK_NAME}" \
    --region "${REGION}" \
    --query "Stacks[0].Outputs[*].[OutputKey,OutputValue,Description]" \
    --output table
