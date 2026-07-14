# GPUQuote

GPUQuote is an open-source CLI for choosing raw GPU rentals for LLM inference. The current foundation provides versioned workload profiles; provider discovery and recommendation will build on the same validated schema.

## Requirements

- Go 1.26 or newer

## Verify

```sh
./scripts/check.sh
```

## List workload presets

```sh
go run ./cmd/gpuquote presets
go run ./cmd/gpuquote presets -output json
```

## Expand a workload

```sh
go run ./cmd/gpuquote workload -preset coding-agent
go run ./cmd/gpuquote workload \
  -preset coding-agent \
  -concurrency 4 \
  -input-tokens 4096 \
  -output json
```

The built-in presets are `personal-chat`, `coding-agent`, `interactive-service`, and `batch-job`. Every assumption is printed and can be overridden explicitly.

GPUQuote does not rent or provision infrastructure.
