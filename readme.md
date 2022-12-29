This is a zero-privilege, zero-capability, zero-permission, zero-container, zero-chroot, tiny OCI image creator.

All it can do is take a base image and add files to it, updating standard metadata (command, environment, ports).

It's has both a CLI command and a public API for use in other Go code.

# Installation

`go install github.com/andrewbaxter/dinker`

# Usage (Command line)

This is an example, where I have a Go binary `hello` in my current directory.

1. Create a config file named `dinker.json` like

   ```json
   {
     "from": "alpine.tar",
     "from_pull": "docker://alpine:3.17.0",
     "dest": "docker://localhost:5000/hello:latest",
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

   This says to base the image on Alpine, add `hello` at the root of the filesystem, and on starting run it.

   If `alpine.tar` doesn't exist locally, pull it from the public registry.

   When built, push it to my private registry at `localhost:5000`.

2. Run `dinker dinker.json`

3. Done!

See `Args` in `dinkerlib/args.go` for the full config documentation. Only `from` `dest` and `files` are mandatory.

For `skopeo` references, see <https://github.com/containers/image/blob/main/docs/containers-transports.5.md> for a full list.

# Usage (Library)

There's one function: `dinkerlib.BuildImage()`

It takes a path to a FROM local oci-image tar file, and an output directory name.

The image is constructed in the directory with the OCI layout, but it isn't put into a tar file.

You can convert it to other formats or upload it using `Image` in `"github.com/containers/image/v5/copy"`, with a source reference generated using `Transport.ParseReference` in `"github.com/containers/image/v5/copy"`.
