Docker images are basically zip files, why should building them take any more privileges than writing files? This is a zero-privilege, zero-capability, zero-permission, zero-container, zero-chroot, tiny OCI image creator.

All it can do is take a base image and add files to it, updating standard metadata (command, environment, ports), and pushing the result somewhere.

It's has both a CLI command and a public API for use in other Go code.

# Installation

`go install github.com/andrewbaxter/dinker`

# Usage

## Terraform (via Terrars)

See <https://github.com/andrewbaxter/terrars/tree/master/helloworld> which has an example of creating a statically linked binary and publishing it as a minimal Docker image.

## Github Actions

In order to push images you need to set `Settings / Code and automation / Actions / General / Workflow permissions` to `Read and write permissions`.

Add this workflow (using rust, for example) in `.github/workflows/build.yaml`:

```yaml
name: Build
on:
  push:
    tags:
      - "*"
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      - uses: actions-rs/toolchain@v1
        with:
          toolchain: stable
          target: x86_64-unknown-linux-musl
      - uses: actions-rs/cargo@v1
        with:
          command: build
          args: --target x86_64-unknown-linux-musl --release
      - env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          cat > dinker.json << ELEPHANT
          {
            "dests": [
              {
                "ref": "docker://ghcr.io/andrewbaxter/somewords:$GITHUB_REF_NAME",
                "user": "$GITHUB_ACTOR",
                "password": "$GITHUB_TOKEN"
              },
              {
                "ref": "docker://ghcr.io/andrewbaxter/somewords:latest",
                "user": "$GITHUB_ACTOR",
                "password": "$GITHUB_TOKEN"
              }
            ],
            "arch": "amd64",
            "os": "linux",
            "files": [
              {
                "source": "target/x86_64-unknown-linux-musl/release/somewords",
                "mode": "755"
              }
            ]
          }
          ELEPHANT
      - uses: docker://ghcr.io/andrewbaxter/dinker:latest
        with:
          args: /dinker dinker.json
```

## Command line

This is an example, where I have a Go binary `hello` in my current directory.

1. Create a config file named `dinker.json` like

   ```json
   {
     "from": "alpine.tar",
     "from_pull": "docker://alpine:3.17.0",
     "dests": [
       {
         "ref": "docker-daemon:hello:latest",
         "host": "unix:///var/run/docker2.sock"
       }
     ],
     "files": [
       {
         "source": "hello",
         "dest": "hello",
         "mode": "755"
       }
     ],
     "cmd": ["/hello"]
   }
   ```

   This says to base the image on Alpine, add `hello` at the root of the filesystem, and by default run it.

   If `alpine.tar` doesn't exist locally, pull it from the public registry.

   When built, push it to my private registry at `localhost:5000`.

2. Run `dinker dinker.json`

3. Done!

## Library

There's one function: `dinkerlib.BuildImage()`

It takes a path to a local oci-image tar file, and an output directory name. It returns a hash of the inputs as used in the interpolation of `dest` on the command line above.

The image is constructed in the directory with the OCI layout, but it isn't put into a tar file or pushed anywhere - you can convert it to other formats or upload it using `Image` in `"github.com/containers/image/v5/copy"`, with a source reference generated using `Transport.ParseReference` in `"github.com/containers/image/v5/copy"`.

# Json reference

The json file has these options:

### Required

- `dest`

  An array of places to save or push the built image.

  The options for the elements are:

  **Required**

  - `ref`
    Where to save the built image, using this format: <https://github.com/containers/image/blob/main/docs/containers-transports.5.md>.

    This is a pattern - you can add the following strings which will be replaced with generated information:

    - `{hash}` - A sha256 sum of all the information used to generate the image (note: this should be stable but has no formal specification and is unrelated to the pushed manifest hash).

    - `{short_hash}` - The first hex digits of the hash

  **Optional**

  - `user`

    Credentials for pushing

  - `password`

    Credentials for pushing

  - `http`

    True if this dest is over http (disable tls validation)

  - `host`

    If using the `docker-daemon` transport which doesn't support host specification, override the default docker daemon.

- `files`

  Files to add to the image. This is an array of objects with these fields:

  - `source` - Required, the location of the file on the building system

  - `dest` - Optional, where to store the file in the image. If not specified, puts it at the root of the image with the same filename as `source`.

  - `mode` - Octal string with file mode (ex: 644)

### Required if no `from`

- `arch`

  Defaults to `from` image architecture

- `os`

  Defaults to `from` image os

### Optional

- `from`

  Add onto the layers from this image (like `FROM` in Docker). This is a path to an OCI image archive tar file. If the file does not exist, it will download the image using `from_pull` and store it here. If not specified, use no base image (this will produce a single layer image with just the specified files).

- `from_pull`

  Where to pull the `from` image if it doesn't exist, using this format: <https://github.com/containers/image/blob/main/docs/containers-transports.5.md>.

- `from_user`

  Credentials for `from_pull` if necessary

- `from_password`

  Credentials for `from_pull` if necessary

- `from_http`

  True if `from_pull` source is over http (disable tls validation)

- `from_host`

  If using the `docker-daemon` transport which doesn't support host specification, override the default docker daemon.

- `add_env`

  Record with string key-value pairs. Add additional default environment values

- `clear_env`

  Boolean. If true, don't inherit environment variables from `from` image.

- `working_dir`

  Container working directory, defaults to `from` image working directory.

- `user`

  User id to run process in container as. Defaults to value in `from` image

- `entrypoint`

  Array of strings. See Docker documentation for details. This is _not_ inherited from the base image.

- `cmd`

  Array of strings. See Docker documentation for details. This is _not_ inherited from the base image.

- `ports`

  Ports within the container to expose. This is an array of records with fields:

  - `port` - Required, the port that the program within the container listens on

  - `transport` - Optional, defaults to `tcp`. `tcp` or `udp`.

  These are _not_ inherited from the base image.

- `labels`

  String key-value record. Arbitrary metadata. These are _not_ inherited from the base image.

- `stop_signal`

  The signal to use when stopping the container. Values like `SIGTERM` `SIGINT` `SIGQUIT`. This is _not_ inherited from the base image.
