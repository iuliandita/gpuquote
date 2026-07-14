# GPUQuote

GPUQuote is an open-source CLI for choosing raw GPU rentals for LLM inference. It provides versioned workload profiles and read-only provider offer discovery on a validated schema.

## Requirements

- Go 1.26 or newer

Install the binary from the repository checkout:

```sh
go install ./cmd/gpuquote
```

## Verify

```sh
./scripts/check.sh
```

## List workload presets

```sh
gpuquote presets
gpuquote presets -output json
```

## Expand a workload

```sh
gpuquote workload -preset coding-agent
gpuquote workload \
  -preset coding-agent \
  -concurrency 4 \
  -input-tokens 4096 \
  -output json
```

The built-in presets are `personal-chat`, `coding-agent`, `interactive-service`, and `batch-job`. Every assumption is printed and can be overridden explicitly.

## Discover provider offers

Use a short-lived environment variable without placing the credential value in shell history:

```zsh
go install ./cmd/gpuquote
read -rs "DIGITALOCEAN_TOKEN?DigitalOcean token: "
export DIGITALOCEAN_TOKEN
print
gpuquote offers -provider digitalocean
gpuquote offers -provider digitalocean -output check
unset DIGITALOCEAN_TOKEN
```

The `offers` command accepts `-provider all|runpod|vastai|lambda|digitalocean`, `-output text|json|check`, and a whole-operation `-timeout` from 1 second through 2 minutes. The defaults are `all`, `text`, and 20 seconds. `check` is an explicit local live probe for stable status and coverage fields; it records no raw response body and is not a CI probe.

Discovery safety and coverage limits:

- Discovery is read-only and never rents or provisions infrastructure.
- Public tests use synthetic fixtures. Live responses remain local in memory.
- DigitalOcean supports narrowly scoped `sizes:read` and `regions:read` tokens. Discovery covers self-service `/v2/sizes` only; per-contract GPU plans are excluded.
- RunPod should use a separate restricted/read key. Its exact minimum GraphQL permission set remains unverified and is reported as a warning. RunPod candidates remain rejected discovery records until price scope and billing increment are documented.
- Lambda and Vast currently do not expose a strictly read-only key for these endpoints. Create separate least-privilege credentials and rotate or revoke them independently.
- Never paste credential values into shell history, startup files, command arguments, or project `.env` files. Prefer secret-manager injection when available, keep the environment lifetime short, and unset the variable after use.
