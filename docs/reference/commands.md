# Commands
| Command | Purpose | Status |
|---------|---------|--------|
| `azath serve` | gRPC `Seal`/`Unseal` server for the Talos KMS protocol | implemented |
| `azath config validate <path>` | Validate server or client config | implemented |
| `azath config new-device --name <name> --config <path>` | Append a configured device and print its generated UUID | implemented |
| `azath config enable-device --name <name> --config <path>` | Enable a device in server config | implemented |
| `azath config disable-device --name <name> --config <path>` | Disable a device in server config | implemented |
| `azath seal` | Seal one secret against an azath endpoint | implemented |
| `azath synology` | Unlock configured Synology shares sequentially | implemented |

`azath --help` and each subcommand's `--help` list flags and arguments. See [Build and test azath](../contributing.md) for development commands and the [operations guides](../README.md#installation-and-operations) for deployment tasks.
