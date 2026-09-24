"""Shared-secret authentication for the internal API (metadata ``x-internal-token``)."""
import hmac

import grpc

TOKEN_METADATA_KEY = "x-internal-token"
# Orchestrators probe health without credentials.
EXEMPT_PREFIXES = ("/grpc.health.v1.Health/",)


def _deny(request, context: grpc.ServicerContext):
    context.abort(grpc.StatusCode.UNAUTHENTICATED, "invalid internal token")


class TokenAuthInterceptor(grpc.ServerInterceptor):
    def __init__(self, token: str) -> None:
        if not token:
            raise ValueError("INTERNAL_TOKEN must be set")
        self._token = token.encode()
        # Every AgentService RPC is unary-unary, so one rejecting handler fits all protected methods.
        self._deny_handler = grpc.unary_unary_rpc_method_handler(_deny)

    def intercept_service(self, continuation, handler_call_details):
        if handler_call_details.method.startswith(EXEMPT_PREFIXES):
            return continuation(handler_call_details)
        supplied = next((value for key, value in handler_call_details.invocation_metadata or ()
                         if key == TOKEN_METADATA_KEY), None)
        if isinstance(supplied, str) and hmac.compare_digest(supplied.encode(), self._token):
            return continuation(handler_call_details)
        return self._deny_handler
