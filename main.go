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
	"path"
	"path/filepath"
	"strconv"
	"strings"

	imagecopy "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/oci/archive"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	tarfs "github.com/nlepage/go-tarfs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

type AbsPath string

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

func (p *AbsPath) Filename() string {
	return filepath.Base(string(*p))
}

func (p *AbsPath) Join(rel string) AbsPath {
	return AbsPath(path.Clean(path.Join(string(*p), rel)))
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
	Dest   *string `json:"dest"`
	// Parsed as octal
	Mode *string `json:"mode"`
}

type BuildImageArgsPort struct {
	Port int `json:"port"`
	// `tcp`
	Transport string `json:"transport"`
}

type BuildImageArgs struct {
	FromPath   AbsPath
	Files      []BuildImageArgsFile
	Cmd        []string
	AddEnv     map[string]string
	ClearEnv   bool
	WorkingDir string
	Ports      []BuildImageArgsPort `json:"ports"`
	DestPath   AbsPath
}

func BuildImage(args BuildImageArgs) error {
	// Combine and write image
	destFd, err := os.Create(args.DestPath.Raw())
	if err != nil {
		return fmt.Errorf("couldn't open %s for writing: %w", args.DestPath, err)
	}
	destTar := tar.NewWriter(destFd)

	writeMemory := func(name string, contents []byte) error {
		if err := destTar.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(contents)),
		}); err != nil {
			return fmt.Errorf("error writing tar header for %s: %w", name, err)
		}
		if _, err := destTar.Write([]byte(contents)); err != nil {
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
		if err := destTar.WriteHeader(&tar.Header{
			Name: blobPath(digest),
			Mode: 0600,
			Size: size,
		}); err != nil {
			return fmt.Errorf("error writing tar header for layer: %w", err)
		}
		_, err = io.Copy(destTar, reader)
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
	{
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
	if err := destTar.Close(); err != nil {
		return fmt.Errorf("error closing tar: %w", err)
	}

	return nil
}

type RegistryImage struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
	// Override default registry URL for operation
	Url      *string `json:"url"`
	User     *string `json:"user"`
	Password *string `json:"password"`
}

type Args struct {
	// Path to OCI FROM image tar
	FromPath AbsPath `json:"from_path"`
	// Pull FROM image from registry (cache at FromPath)
	FromRegistry *RegistryImage `json:"from_registry"`
	// Save image to file as OCI tar
	DestPath *AbsPath `json:"dest_path"`
	// Push to registry - if URL is none, registers with local docker daemon
	DestRegistry *RegistryImage       `json:"dest_registry"`
	Files        []BuildImageArgsFile `json:"files"`
	Cmd          []string             `json:"cmd"`
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

	if args.FromPath == "" {
		return fmt.Errorf("missing FROM path in config")
	}
	if len(args.Files) == 0 {
		return fmt.Errorf("missing files to add in config")
	}
	if (args.DestPath == nil || *args.DestPath == "") && args.DestRegistry == nil {
		return fmt.Errorf("missing dest path or dest registry")
	}

	policy, err := signature.DefaultPolicy(nil)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context signature: %w", err)
	}
	policyContext, err := signature.NewPolicyContext(policy)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context: %w", err)
	}

	if !args.FromPath.Exists() {
		if args.FromRegistry != nil {
			var url string
			if args.FromRegistry.Url == nil {
				url = ""
			} else {
				url = fmt.Sprintf("%s/", *args.FromRegistry.Url)
			}
			sourceRef, err := docker.Transport.ParseReference(fmt.Sprintf(
				"//%s%s:%s",
				url,
				args.FromRegistry.Name,
				args.FromRegistry.Tag,
			))
			if err != nil {
				panic(err)
			}
			destRef, err := archive.Transport.ParseReference(args.FromPath.Raw())
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
							Username: def(args.FromRegistry.User, ""),
							Password: def(args.FromRegistry.Password, ""),
						},
					},
				},
			)
			if err != nil {
				return fmt.Errorf("error pulling FROM image %s: %w", args.FromRegistry.Name, err)
			}
		} else {
			return fmt.Errorf("no FROM image exists at %s, and no registry target configured to pull from", args.FromPath)
		}
	}
	var destPath AbsPath
	if args.DestPath != nil {
		destPath = *args.DestPath
	} else {
		t, err := os.CreateTemp("", ".dinker-image-*")
		if err != nil {
			return fmt.Errorf("unable to create temp file to write generated image to: %w", err)
		}
		t0, err := filepath.Abs(t.Name())
		if err != nil {
			panic(err)
		}
		destPath = AbsPath(t0)
		if err := t.Close(); err != nil {
			log.Printf("Couldn't close temp file at %s: %s", destPath, err)
		}
		defer func() {
			if err := os.Remove(destPath.Raw()); err != nil {
				log.Printf("Error deleting temp image file at %s: %s", destPath, err)
			}
		}()
	}
	if err := BuildImage(BuildImageArgs{
		FromPath:   args.FromPath,
		Files:      args.Files,
		Cmd:        args.Cmd,
		AddEnv:     args.AddEnv,
		ClearEnv:   args.ClearEnv,
		WorkingDir: args.WorkingDir,
		DestPath:   destPath,
	}); err != nil {
		return fmt.Errorf("error building image: %w", err)
	}
	if args.DestRegistry != nil {
		sourceRef, err := archive.Transport.ParseReference(destPath.Raw())
		if err != nil {
			panic(err)
		}
		destRef, err := docker.Transport.ParseReference(fmt.Sprintf(
			"//%s/%s:%s",
			def(args.DestRegistry.Url, "localhost"),
			args.DestRegistry.Name,
			args.DestRegistry.Tag,
		))
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
						Username: def(args.DestRegistry.User, ""),
						Password: def(args.DestRegistry.Password, ""),
					},
				},
			},
		)
		if err != nil {
			return fmt.Errorf("error uploading image: %w", err)
		}
	}
	return nil
}

func main() {
	err := main0()
	if err != nil {
		log.Fatalf("Exiting with fatal error: %s", err)
	}
}
