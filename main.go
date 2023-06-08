package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewbaxter/dinker/dinkerlib"
	imagecopy "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/oci/archive"
	ocidir "github.com/containers/image/v5/oci/layout"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
)

type RegistryCreds struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

type ConfigDest struct {
	Ref      string `json:"ref"`
	User     string `json:"user"`
	Password string `json:"password"`
	Http     bool   `json:"http"`
}

type Config struct {
	From         dinkerlib.AbsPath              `json:"from"`
	FromPull     string                         `json:"from_pull"`
	FromUser     string                         `json:"from_user"`
	FromPassword string                         `json:"from_password"`
	FromHttp     bool                           `json:"from_http"`
	Dests        []ConfigDest                   `json:"dests"`
	Architecture string                         `json:"arch"`
	Os           string                         `json:"os"`
	Files        []dinkerlib.BuildImageArgsFile `json:"files"`
	AddEnv       map[string]string              `json:"add_env"`
	ClearEnv     bool                           `json:"clear_env"`
	WorkingDir   string                         `json:"working_dir"`
	User         string                         `json:"user"`
	Entrypoint   []string                       `json:"entrypoint"`
	Cmd          []string                       `json:"cmd"`
	Ports        []dinkerlib.BuildImageArgsPort `json:"ports"`
	Labels       map[string]string              `json:"labels"`
	StopSignal   string                         `json:"stop_signal"`
}

func main0() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("must have one argument: path to config json file")
	}
	var args0 []byte
	if os.Args[1] == "-" {
		var err error
		args0, err = ioutil.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("error reading config from stdin: %w", err)
		}
	} else {
		var err error
		args0, err = ioutil.ReadFile(os.Args[1])
		if err != nil {
			return fmt.Errorf("error reading config at %s: %w", os.Args[1], err)
		}
	}
	var config Config
	err := json.Unmarshal(args0, &config)
	if err != nil {
		return fmt.Errorf("error parsing config json at %s: %w", os.Args[1], err)
	}

	if config.From == "" && config.Os == "" && config.Architecture == "" {
		return fmt.Errorf("missing FROM ref in config")
	}
	if len(config.Files) == 0 {
		return fmt.Errorf("missing files to add in config")
	}
	if len(config.Dests) == 0 {
		return fmt.Errorf("missing dest in config")
	}

	policy, err := signature.DefaultPolicy(nil)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context signature: %w", err)
	}
	policyContext, err := signature.NewPolicyContext(policy)
	if err != nil {
		return fmt.Errorf("error setting up docker registry client policy context: %w", err)
	}

	if config.From != "" && !config.From.Exists() {
		if config.FromPull == "" {
			return fmt.Errorf("no FROM image exists at %s, and no pull ref configured to pull from", config.From)
		}
		log.Printf("Pulling from image...")
		sourceRef, err := alltransports.ParseImageName(config.FromPull)
		if err != nil {
			return fmt.Errorf("error parsing FROM pull ref %s: %w", config.FromPull, err)
		}
		destRef, err := archive.Transport.ParseReference(config.From.Raw())
		if err != nil {
			panic(err)
		}
		var noHttpVerify types.OptionalBool
		if config.FromHttp {
			noHttpVerify = types.OptionalBoolTrue
		}
		_, err = imagecopy.Image(
			context.TODO(),
			policyContext,
			destRef,
			sourceRef,
			&imagecopy.Options{
				SourceCtx: &types.SystemContext{
					DockerInsecureSkipTLSVerify: noHttpVerify,
					DockerAuthConfig: &types.DockerAuthConfig{
						Username: config.FromUser,
						Password: config.FromPassword,
					},
				},
			},
		)
		if err != nil {
			return fmt.Errorf("error pulling FROM image %s: %w", config.FromPull, err)
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
	destDirPath := dinkerlib.AbsPath(t0)
	defer func() {
		if err := os.RemoveAll(destDirPath.Raw()); err != nil {
			log.Printf("Error deleting temp image dir at %s: %s", destDirPath, err)
		}
	}()

	log.Printf("Building image...")
	hash, err := dinkerlib.BuildImage(dinkerlib.BuildImageArgs{
		FromPath:     config.From,
		Architecture: config.Architecture,
		Os:           config.Os,
		Files:        config.Files,
		ClearEnv:     config.ClearEnv,
		AddEnv:       config.AddEnv,
		WorkingDir:   config.WorkingDir,
		User:         config.User,
		Entrypoint:   config.Entrypoint,
		Cmd:          config.Cmd,
		Ports:        config.Ports,
		StopSignal:   config.StopSignal,
		Labels:       config.Labels,
		DestDirPath:  destDirPath,
	})
	if err != nil {
		return fmt.Errorf("error building image: %w", err)
	}
	log.Printf("Building image... done.")
	sourceRef, err := ocidir.Transport.ParseReference(destDirPath.Raw())
	if err != nil {
		panic(err)
	}

	for i, dest := range config.Dests {
		if dest.Ref == "" {
			log.Printf("Warning! Missing ref in dest %d, skipping", i)
			continue
		}
		destString := dest.Ref
		for k, v := range map[string]string{
			"hash":       hash,
			"short_hash": hash[:8],
		} {
			destString = strings.ReplaceAll(destString, fmt.Sprintf("{%s}", k), v)
		}
		destRef, err := alltransports.ParseImageName(destString)
		if err != nil {
			return fmt.Errorf("invalid dest image ref %s: %w", dest.Ref, err)
		}

		log.Printf("Pushing to %s...", destString)
		var noHttpVerify types.OptionalBool
		if dest.Http {
			noHttpVerify = types.OptionalBoolTrue
		}
		destSysCtx := types.SystemContext{
			DockerInsecureSkipTLSVerify: noHttpVerify,
			DockerAuthConfig: &types.DockerAuthConfig{
				Username: dest.User,
				Password: dest.Password,
			},
		}
		destImg, err := destRef.NewImageDestination(context.TODO(), &destSysCtx)
		if err != nil {
			panic(err)
		}
		manifestFormat := ""
		for _, format := range destImg.SupportedManifestMIMETypes() {
			// Prefer docker manifest
			if format == manifest.DockerV2Schema2MediaType {
				manifestFormat = format
			}
		}
		_, err = imagecopy.Image(
			context.TODO(),
			policyContext,
			destRef,
			sourceRef,
			&imagecopy.Options{
				ForceManifestMIMEType: manifestFormat,
				DestinationCtx:        &destSysCtx,
			},
		)
		if err != nil {
			return fmt.Errorf("error uploading image: %w", err)
		}
		log.Printf("Pushing to %s... done.", dest.Ref)
	}
	return nil
}

func main() {
	err := main0()
	if err != nil {
		log.Fatalf("Exiting with fatal error: %s", err)
	}
}
