gRPC Service Config
===================

[gRPC service config](https://github.com/grpc/grpc/blob/master/doc/service_config.md) without service discovery, by specifying the service config via file annotations and including it in the generated code.

How to
======

Step 1: Add a service config file annotation to your package
------------------------------------------------------------

For example [einride/serviceconfig/example/v1/default_service_config.proto](./einride/serviceconfig/example/v1/default_service_config.proto):

```proto
syntax = "proto3";

package einride.serviceconfig.example.v1;

import "einride/serviceconfig/v1/annotations.proto";

option (einride.serviceconfig.v1.default_service_config) = {
  method_config: {
    name: {}
    timeout: {
      seconds: 10
    }
    retry_policy: {
      initial_backoff: {
        nanos: 200000000 // 0.2s
      }
      max_backoff: {
        seconds: 60
      }
      max_attempts: 5
      backoff_multiplier: 2
      retryable_status_codes: UNAVAILABLE
      retryable_status_codes: UNKNOWN
    }
  }
};
```

Step 2: Run the protoc plugin
-----------------------------

Use the optional `validate` option to validate that the service config format is valid. Use the optional `required` option to require every service to have a service config.

```bash
protoc
  -I src \
  --go_out=gen/go \
  --go-grpc_out=gen/go \
  --go-grpc-service-config_out=gen/go \
  --go-grpc-service-config_opt=validate=true \
  --go-grpc-service-config_opt=required=true
```

Your generated code output will now have a Go file corresponding to every service config JSON file.

For example [example_grpc_service_config.pb.go](./internal/gen/proto/einride/serviceconfig/example/v1/example_grpc_service_config.pb.go):

```go
package examplev1

// DefaultServiceConfig is the default service config for all services in the package.
const DefaultServiceConfig = `{
  "methodConfig": [
    {
      "name": [{}],
      "timeout": "10s",
      "retryPolicy": {
        "initialBackoff": "0.200s",
        "maxBackoff": "60s",
        "maxAttempts": 5,
        "backoffMultiplier": 1.3,
        "retryableStatusCodes": ["UNAVAILABLE"]
      }
    }
  ]
}
`
```

Step 3: Use your bundled service config when dialing
----------------------------------------------------

```go
conn, err := grpc.DialContext(
	ctx,
	"example.com:443",
	grpc.WithDefaultServiceConfig(examplev1.DefaultServiceConfig),
)
```
