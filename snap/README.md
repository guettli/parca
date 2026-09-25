# Parca Snap

This directory contains files used to build the [Parca](https://parca.dev) snap.

## Parca App

The snap provides a base `parca` app, which can be executed as per the upstream documentation. The
snap is strictly confined, and thus can only read from certain locations on the filesystem. The
default config file from the upstream is populated in `/var/snap/parca/current/parca.yaml`.

The snap does have access to `/etc/parca/parca.yaml` and your home directory by default, and as
such can read config files from these locations.

You can start Parca manually like so:

```bash
# Install from the 'edge' channel
$ sudo snap install parca --channel edge

# Start Parca with the default config file from the SNAP_DATA directory
$ parca --config-file=/var/snap/parca/current/parca.yaml

# Or grab the default config from the Parca repo
$ wget -qO ~/parca.yaml https://raw.githubusercontent.com/parca-dev/parca/main/parca.yaml
$ parca --config-file=~/parca.yaml
```

## Parca Service

Additionally, the snap provides a service for Parca with a limited set of configuration options.
The service uses the default `clickhouse` storage backend for profile data, so it needs a
ClickHouse server reachable at `localhost:9000` — without one Parca exits with
`failed to connect to ClickHouse`. The snap is built without cgo, so the embedded DuckDB
backend is not compiled in and cannot be used instead.

You can start the service like so:

```bash
$ snap start parca
```

There are a small number of config options:

| Name                 | Valid Options                    | Default | Description                                                                   |
| :------------------- | :------------------------------- | :------ | :---------------------------------------------------------------------------- |
| `enable-persistence` | `true`, `false`                  | `false` | Persist the local metastore to disk under `/var/snap/parca/current/profiles/` |
| `log-level`          | `error`, `warn`, `info`, `debug` | `info`  | Log level for Parca                                                           |
| `port`               | 1024 > `int` > 65534             | `7070`  | Port for Parca server to listen on                                            |

Config options can be set with `sudo snap set parca <option>=<value>`
