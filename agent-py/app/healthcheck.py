"""Container health probe over the standard gRPC health protocol: ``python -m app.healthcheck``.
Exits 0 only when the local server reports SERVING."""
import os
import sys

import grpc
from grpc_health.v1 import health_pb2, health_pb2_grpc


def main() -> int:
    target = f"127.0.0.1:{os.environ.get('AGENT_GRPC_PORT', '9091')}"
    try:
        with grpc.insecure_channel(target) as channel:
            response = health_pb2_grpc.HealthStub(channel).Check(health_pb2.HealthCheckRequest(), timeout=3)
    except grpc.RpcError as e:
        print(f"health check failed: {e.code().name}", file=sys.stderr)
        return 1
    return 0 if response.status == health_pb2.HealthCheckResponse.SERVING else 1


if __name__ == "__main__":
    sys.exit(main())
