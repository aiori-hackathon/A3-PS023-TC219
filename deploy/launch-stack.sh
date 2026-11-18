#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_FILE="${SCRIPT_DIR}/cloudformation.yaml"
REGION="ap-south-1"
STACK_NAME="vm-controller-3node"

echo "Deploying CloudFormation stack: ${STACK_NAME} in ${REGION}..."

aws cloudformation create-stack \
  --stack-name "${STACK_NAME}" \
  --template-body "file://${TEMPLATE_FILE}" \
  --parameters \
      ParameterKey=WorkerInstanceType,ParameterValue=c7i-flex.xlarge \
      ParameterKey=ControllerInstanceType,ParameterValue=t3.medium \
      ParameterKey=KeyName,ParameterValue=vm-key-mumbai \
      ParameterKey=VpcId,ParameterValue=vpc-054cfd8f626161678 \
      ParameterKey=SubnetId,ParameterValue=subnet-03a20229f10a79604 \
      ParameterKey=IamInstanceProfile,ParameterValue=PS023-Worker-Role \
      ParameterKey=S3BucketName,ParameterValue=aiori-3-linux-isos \
  --capabilities CAPABILITY_IAM \
  --region "${REGION}"

echo "Stack creation initiated! Waiting for completion (~2-3 minutes)..."
aws cloudformation wait stack-create-complete --stack-name "${STACK_NAME}" --region "${REGION}"

echo ""
echo "=================================================================="
echo " 3-Node Cluster Deployment Complete! Stack Outputs:"
echo "=================================================================="
aws cloudformation describe-stacks \
  --stack-name "${STACK_NAME}" \
  --region "${REGION}" \
  --query "Stacks[0].Outputs[*].[OutputKey,OutputValue]" \
  --output table
