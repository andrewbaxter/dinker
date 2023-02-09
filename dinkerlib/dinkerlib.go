package dinkerlib

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"

	tarfs "github.com/nlepage/go-tarfs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

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
			mode, err := strconv.ParseInt(Def(f.Mode, "644"), 8, 32)
			if err != nil {
				return fmt.Errorf("file %s mode %s is not valid octal: %w", f.Source, f.Mode, err)
			}
			if err := destTar.WriteHeader(&tar.Header{
				Name: strings.TrimPrefix(Def(f.Dest, f.Source.Filename()), "/"),
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
	if args.FromPath != "" {
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
			ports[fmt.Sprintf("%d/%s", p.Port, Def(p.Transport, "tcp"))] = struct{}{}
		}
	}

	// Write remaining meta files
	imageConfigDigest, imageConfig := buildJson(imagespec.Image{
		Architecture: Def(args.Architecture, fromConfig.Architecture),
		OS:           Def(args.Os, fromConfig.OS),
		Config: imagespec.ImageConfig{
			Env:          env,
			WorkingDir:   Def(args.WorkingDir, fromConfig.Config.WorkingDir),
			User:         Def(args.User, fromConfig.Config.User),
			Entrypoint:   args.Entrypoint,
			Cmd:          args.Cmd,
			ExposedPorts: ports,
			StopSignal:   args.StopSignal,
			Labels:       args.Labels,
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
