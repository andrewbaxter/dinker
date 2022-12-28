package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	imagecopy "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/oci/archive"
	ocidir "github.com/containers/image/v5/oci/layout"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	tarfs "github.com/nlepage/go-tarfs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

type AbsPath string

// During json unmarshaling, relative paths are based on the working directory of dinker
func (s *AbsPath) UnmarshalText(text []byte) error {
	p, err := filepath.Abs(string(text))
	if err != nil {
		return err
	}
	*s = AbsPath(p)
	return nil
}

func (p *AbsPath) String() string {
	return string(*p)
}

func (p *AbsPath) Raw() string {
	return string(*p)
}

func (p *AbsPath) Parent() *AbsPath {
	parent, _ := filepath.Split(p.Raw())
	out := AbsPath(parent)
	return &out
}

func (p *AbsPath) Filename() string {
	return filepath.Base(string(*p))
}

func (p *AbsPath) Join(rel string) *AbsPath {
	if filepath.IsAbs(rel) {
		panic("join path abs: " + rel)
	}
	out := AbsPath(filepath.Clean(filepath.Join(string(*p), rel)))
	return &out
}

func (p *AbsPath) Exists() bool {
	_, err := os.Stat(p.Raw())
	return !os.IsNotExist(err)
}

func def[T any](v *T, alt T) T {
	if v == nil {
		return alt
	} else {
		return *v
	}
}

func readTfsJson[T any](tfs fs.FS, p string) (out T, err error) {
	f, err := tfs.Open(p)
	if err != nil {
		return out, fmt.Errorf("unable to open file %s in tar: %w", p, err)
	}
	contents, err := ioutil.ReadAll(f)
	if err != nil {
		return out, fmt.Errorf("error reading file %s from tar: %w", p, err)
	}
	err = json.Unmarshal(contents, &out)
	if err != nil {
		return out, fmt.Errorf("error unmarshaling %s as json: %w", p, err)
	}
	return
}

type BuildImageArgsFile struct {
	Source AbsPath `json:"source"`
	// Defaults to the filename of Source in /. Ex: if source is `a/b/c` the resulting image will have the file at `/c`
	Dest *string `json:"dest"`
	// Parsed as octal, defaults to 0644
	Mode *string `json:"mode"`
}

type BuildImageArgsPort struct {
	Port int `json:"port"`
	// `tcp`
	Transport string `json:"transport"`
}

type BuildImageArgs struct {
	FromPath    AbsPath
	Files       []BuildImageArgsFile
	Cmd         []string
	AddEnv      map[string]string
	ClearEnv    bool
	WorkingDir  string
	Ports       []BuildImageArgsPort `json:"ports"`
	DestDirPath AbsPath
}

func BuildImage(args BuildImageArgs) error {
	// Combine and write image
	if err := os.MkdirAll(args.DestDirPath.Raw(), 0o755); err != nil {
		return fmt.Errorf("error creating staging dir for image at %s: %w", args.DestDirPath, err)
	}

	writeMemory := func(name string, contents []byte) error {
		if err := ioutil.WriteFile(args.DestDirPath.Join(name).Raw(), contents, 0o600); err != nil {
			return fmt.Errorf("error writing tar file %s: %w", name, err)
		}
		return nil
	}
	blobPath := func(digest digest.Digest) string {
		return fmt.Sprintf("blobs/%s/%s", digest.Algorithm().String(), digest.Hex())
	}
	writeBlob := func(digest digest.Digest, contents []byte) error {
		return writeMemory(blobPath(digest), contents)
	}
	buildJson := func(contents any) (digest.Digest, []byte) {
		contents1, err := json.Marshal(contents)
		if err != nil {
			panic(err)
		}
		d := sha256.New()
		_, _ = d.Write(contents1)
		return digest.NewDigest(
			digest.SHA256,
			d,
		), contents1
	}
	writeJson := func(name string, contents any) error {
		contents1, err := json.Marshal(contents)
		if err != nil {
			panic(err)
		}
		return writeMemory(name, contents1)
	}
	writeBlobReader := func(digest digest.Digest, size int64, reader io.Reader) error {
		p := args.DestDirPath.Join(blobPath(digest))
		if err := os.MkdirAll(p.Parent().Raw(), 0o755); err != nil {
			return fmt.Errorf("unable to create parent directories for image file %s: %w", p, err)
		}
		f, err := os.Create(p.Raw())
		if err != nil {
			return fmt.Errorf("error creating %s: %w", p, err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				log.Printf("Error closing %s: %s", p, err)
			}
		}()
		_, err = io.Copy(f, reader)
		if err != nil {
			return fmt.Errorf("error writing layer to tar: %w", err)
		}
		return nil
	}

	// Write layout file
	if err := writeJson("oci-layout", imagespec.ImageLayout{
		Version: "1.0.0",
	}); err != nil {
		return err
	}

	layerDiffIds := []digest.Digest{}
	type LayerMeta struct {
		type_  string
		digest digest.Digest
		size   int64
	}
	layerMetas := []imagespec.Descriptor{}

	// Write own layer
	{
		// Build image in temp file
		tmpLayer, err := os.CreateTemp("", ".dinker-layer-*") // todo delete
		if err != nil {
			return fmt.Errorf("error creating temp file for new layer: %w", err)
		}
		defer func() {
			err := os.Remove(tmpLayer.Name())
			if err != nil {
				log.Printf("Warning: failed to remove layer temp file %s: %s", tmpLayer.Name(), err)
			}
		}()
		uncompressedDigester := sha256.New()
		compressedDigester := sha256.New()
		gzWriter := gzip.NewWriter(io.MultiWriter(
			compressedDigester,
			tmpLayer,
		))
		destTar := tar.NewWriter(io.MultiWriter(
			uncompressedDigester,
			gzWriter,
		))
		for _, f := range args.Files {
			stat, err := os.Stat(f.Source.Raw())
			if err != nil {
				return fmt.Errorf("error looking up metadata for layer file %s: %w", f.Source, err)
			}
			mode, err := strconv.ParseInt(def(f.Mode, "644"), 8, 32)
			if err != nil {
				return fmt.Errorf("file %s mode %s is not valid octal: %w", f.Source, *f.Mode, err)
			}
			if err := destTar.WriteHeader(&tar.Header{
				Name: strings.TrimPrefix(def(f.Dest, f.Source.Filename()), "/"),
				Mode: mode,
				Size: stat.Size(),
			}); err != nil {
				return fmt.Errorf("error writing tar header for %s: %w", f.Source, err)
			}
			fSource, err := os.Open(f.Source.Raw())
			if err != nil {
				return fmt.Errorf("error opening source file %s for adding to layer: %w", f.Source, err)
			}
			_, err = io.Copy(destTar, fSource)
			if err != nil {
				return fmt.Errorf("error copying data from %s: %w", f.Source, err)
			}
			err = fSource.Close()
			if err != nil {
				return fmt.Errorf("error closing %s after reading: %w", f.Source, err)
			}
		}
		if err := destTar.Close(); err != nil {
			return fmt.Errorf("error closing layer tar: %w", err)
		}
		if err := gzWriter.Close(); err != nil {
			return fmt.Errorf("error closing layer tar gz: %w", err)
		}
		stat, err := tmpLayer.Stat()
		if err != nil {
			return fmt.Errorf("error reading temp layer file metadata: %w", err)
		}

		layerDigest := digest.NewDigest(digest.SHA256, compressedDigester)
		layerMetas = append(layerMetas, imagespec.Descriptor{
			MediaType: imagespec.MediaTypeImageLayerGzip,
			Digest:    layerDigest,
			Size:      stat.Size(),
		})
		layerDiffIds = append(layerDiffIds, digest.NewDigest(digest.SHA256, uncompressedDigester))

		_, err = tmpLayer.Seek(0, 0)
		if err != nil {
			panic(err)
		}
		err = writeBlobReader(layerDigest, stat.Size(), tmpLayer)
		if err != nil {
			return err
		}
	}

	// Write `from` layers, pull `from` info
	var fromConfig imagespec.Image
	if err := func() error {
		tf, err := os.Open(args.FromPath.Raw())
		if err != nil {
			return fmt.Errorf("unable to open `from` image: %w", err)
		}
		defer tf.Close()

		tfs, err := tarfs.New(tf)
		if err != nil {
			return fmt.Errorf("unable to open `from` image as tar: %w", err)
		}

		index, err := readTfsJson[imagespec.Index](tfs, "index.json")
		if err != nil {
			return err
		}
		for _, m := range index.Manifests {
			if m.MediaType != imagespec.MediaTypeImageManifest {
				continue
			}

			manifest, err := readTfsJson[imagespec.Manifest](tfs, blobPath(m.Digest))
			if err != nil {
				return fmt.Errorf("unable to find manifest %s referenced in tar index: %w", m.Digest, err)
			}
			layerMetas = append(layerMetas, manifest.Layers...)
			for _, layer := range manifest.Layers {
				source, err := tfs.Open(blobPath(layer.Digest))
				if err != nil {
					return fmt.Errorf("error opening layer %s referenced in image manifest: %w", layer.Digest, err)
				}
				err = writeBlobReader(layer.Digest, layer.Size, source)
				if err != nil {
					return fmt.Errorf("error copying `from` layer %s to new image: %w", layer.Digest, err)
				}
			}

			fromConfig, err = readTfsJson[imagespec.Image](tfs, blobPath(manifest.Config.Digest))
			if err != nil {
				return fmt.Errorf("unable to find config %s referenced in image manifest: %w", manifest.Config.Digest, err)
			}
			layerDiffIds = append(layerDiffIds, fromConfig.RootFS.DiffIDs...)
		}
		return nil
	}(); err != nil {
		return fmt.Errorf("error reading FROM image %s: %w", args.FromPath, err)
	}
	env := []string{}
	if !args.ClearEnv {
		env = append(env, fromConfig.Config.Env...)
	}
	for k, v := range args.AddEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	ports := map[string]struct{}{}
	if len(args.Ports) != 0 {
		for _, p := range args.Ports {
			ports[fmt.Sprintf("%d/%s", p.Port, p.Transport)] = struct{}{}
		}
	}

	// Write remaining meta files
	imageConfigDigest, imageConfig := buildJson(imagespec.Image{
		Architecture: fromConfig.Architecture,
		OS:           fromConfig.OS,
		Config: imagespec.ImageConfig{
			Env:          env,
			Cmd:          args.Cmd,
			WorkingDir:   args.WorkingDir,
			ExposedPorts: ports,
		},
		RootFS: imagespec.RootFS{
			Type:    "layers",
			DiffIDs: layerDiffIds,
		},
	})
	if err := writeBlob(imageConfigDigest, imageConfig); err != nil {
		return err
	}
	imageManifestDigest, imageManifest := buildJson(imagespec.Manifest{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: imagespec.MediaTypeImageManifest,
		Config: imagespec.Descriptor{
			MediaType: imagespec.MediaTypeImageConfig,
			Digest:    imageConfigDigest,
			Size:      int64(len(imageConfig)),
		},
		Layers: layerMetas,
	})
	if err := writeBlob(imageManifestDigest, imageManifest); err != nil {
		return err
	}
	if err := writeJson("index.json", imagespec.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		Manifests: []imagespec.Descriptor{
			{
				MediaType: imagespec.MediaTypeImageManifest,
				Digest:    imageManifestDigest,
				Size:      int64(len(imageManifest)),
			},
		},
	}); err != nil {
		return err
	}

	return nil
}

type RegistryCreds struct {
	User     *string `json:"user"`
	Password *string `json:"password"`
}

type Args struct {
	// Path to FROM oci image. If not present, pulls using FromPull
	From AbsPath `json:"from"`
	// Pull FROM oci image from this ref if it doesn't exist locally (skopeo-style)
	FromPull string `json:"from_pull"`
	// Credentials to pull FROM if necessary
	FromCreds RegistryCreds `json:"from_registry"`
	// Save image to ref (skopeo-style)
	Dest string
	// Credentials to push to dest if necessary
	DestCreds RegistryCreds        `json:"dest_registry"`
	Files     []BuildImageArgsFile `json:"files"`
	Cmd       []string             `json:"cmd"`
	// Add additional default environment values
	AddEnv map[string]string `json:"add_env"`
	// Clear inherited environment from FROM image
	ClearEnv   bool                 `json:"clear_env"`
	Ports      []BuildImageArgsPort `json:"ports"`
	WorkingDir string               `json:"working_dir"`
}

func main0() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("must have one argument: path to config json file")
	}
	var args Args
	args0, err := ioutil.ReadFile(os.Args[1])
	if err != nil {
		return fmt.Errorf("error reading config at %s: %w", os.Args[1], err)
	}
	err = json.Unmarshal(args0, &args)
	if err != nil {
		return fmt.Errorf("error parsing config json at %s: %w", os.Args[1], err)
	}

	if args.From == "" {
		return fmt.Errorf("missing FROM ref in config")
	}
	if len(args.Files) == 0 {
		return fmt.Errorf("missing files to add in config")
	}
	if args.Dest == "" {
		return fmt.Errorf("missing dest ref in config")
	}

	policy, err := signature.DefaultPolicy(nil)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context signature: %w", err)
	}
	policyContext, err := signature.NewPolicyContext(policy)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context: %w", err)
	}

	destRef, err := alltransports.ParseImageName(args.Dest)
	if err != nil {
		return fmt.Errorf("invalid dest image ref %s: %w", args.From, err)
	}

	if !args.From.Exists() {
		if args.FromPull == "" {
			return fmt.Errorf("no FROM image exists at %s, and no pull ref configured to pull from", args.From)
		}
		log.Printf("Pulling from image...")
		sourceRef, err := alltransports.ParseImageName(args.FromPull)
		if err != nil {
			return fmt.Errorf("error parsing FROM pull ref %s: %w", args.FromPull, err)
		}
		destRef, err := archive.Transport.ParseReference(args.From.Raw())
		if err != nil {
			panic(err)
		}
		_, err = imagecopy.Image(
			context.TODO(),
			policyContext,
			destRef,
			sourceRef,
			&imagecopy.Options{
				SourceCtx: &types.SystemContext{
					DockerAuthConfig: &types.DockerAuthConfig{
						Username: def(args.FromCreds.User, ""),
						Password: def(args.FromCreds.Password, ""),
					},
				},
			},
		)
		if err != nil {
			return fmt.Errorf("error pulling FROM image %s: %w", args.FromPull, err)
		}
		log.Printf("Pulling from image... done.")
	}

	t, err := os.MkdirTemp("", ".dinker-image-*")
	if err != nil {
		return fmt.Errorf("unable to create temp file to write generated image to: %w", err)
	}
	t0, err := filepath.Abs(t)
	if err != nil {
		panic(err)
	}
	destDirPath := AbsPath(t0)
	defer func() {
		if err := os.RemoveAll(destDirPath.Raw()); err != nil {
			log.Printf("Error deleting temp image dir at %s: %s", destDirPath, err)
		}
	}()

	log.Printf("Building image...")
	if err := BuildImage(BuildImageArgs{
		FromPath:    args.From,
		Files:       args.Files,
		Cmd:         args.Cmd,
		AddEnv:      args.AddEnv,
		ClearEnv:    args.ClearEnv,
		WorkingDir:  args.WorkingDir,
		DestDirPath: destDirPath,
	}); err != nil {
		return fmt.Errorf("error building image: %w", err)
	}
	log.Printf("Building image... done.")
	sourceRef, err := ocidir.Transport.ParseReference(destDirPath.Raw())
	if err != nil {
		panic(err)
	}

	log.Printf("Pushing to %s...", args.Dest)
	_, err = imagecopy.Image(
		context.TODO(),
		policyContext,
		destRef,
		sourceRef,
		&imagecopy.Options{
			DestinationCtx: &types.SystemContext{
				DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
				DockerAuthConfig: &types.DockerAuthConfig{
					Username: def(args.DestCreds.User, ""),
					Password: def(args.DestCreds.Password, ""),
				},
			},
		},
	)
	if err != nil {
		return fmt.Errorf("error uploading image: %w", err)
	}
	log.Printf("Pushing to %s... done.", args.Dest)
	return nil
}

func main() {
	err := main0()
	if err != nil {
		log.Fatalf("Exiting with fatal error: %s", err)
	}
}
