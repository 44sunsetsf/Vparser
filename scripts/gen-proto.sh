#!/usr/bin/env bash
# Regenerate gRPC stubs from proto/ into server-go/gen and agent-py/app/gen (generated code is committed).
# Generator versions are pinned (CI checks the committed stubs byte-for-byte):
#   protoc 25.3, protoc-gen-go v1.36.10, protoc-gen-go-grpc v1.6.0, grpcio-tools 1.84.0
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="$PATH:$(go env GOPATH)/bin"

rm -rf server-go/gen && mkdir -p server-go/gen
protoc -I proto --go_out=server-go/gen --go_opt=module=dovideo/server/gen \
  --go-grpc_out=server-go/gen --go-grpc_opt=module=dovideo/server/gen \
  proto/agent/v1/agent.proto

rm -rf agent-py/app/gen && mkdir -p agent-py/app/gen
(cd agent-py && uv run --with grpcio-tools==1.84.0 python -m grpc_tools.protoc -I ../proto \
  --python_out=app/gen --pyi_out=app/gen --grpc_python_out=app/gen ../proto/agent/v1/agent.proto)
# grpc_tools emits absolute imports (`from agent.v1 import ...`); make them package-relative.
find agent-py/app/gen -name '*.py' -exec sed -i.bak 's/^from agent\.v1 import/from . import/' {} + && find agent-py/app/gen -name '*.bak' -delete
touch agent-py/app/gen/__init__.py agent-py/app/gen/agent/__init__.py agent-py/app/gen/agent/v1/__init__.py
echo "stubs regenerated"
