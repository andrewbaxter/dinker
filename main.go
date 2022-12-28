package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/andrewbaxter/dinker/dinkerlib"
	imagecopy "github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/oci/archive"
	ocidir "github.com/containers/image/v5/oci/layout"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
)

type RegistryCreds struct {
	User     *string `json:"user"`
	Password *string `json:"password"`
}

type Args struct {
	// Path to FROM oci image. If not present, pulls using FromPull
	From dinkerlib.AbsPath `json:"from"`
	// Pull FROM oci image from this ref if it doesn't exist locally (skopeo-style)
	FromPull string `json:"from_pull"`
	// Credentials to pull FROM if necessary
	FromCreds RegistryCreds `json:"from_registry"`
	// Save image to ref (skopeo-style)
	Dest string
	// Credentials to push to dest if necessary
	DestCreds RegistryCreds                  `json:"dest_registry"`
	Files     []dinkerlib.BuildImageArgsFile `json:"files"`
	Cmd       []string                       `json:"cmd"`
	// Add additional default environment values
	AddEnv map[string]string `json:"add_env"`
	// Clear inherited environment from FROM image
	ClearEnv   bool                           `json:"clear_env"`
	Ports      []dinkerlib.BuildImageArgsPort `json:"ports"`
	WorkingDir string                         `json:"working_dir"`
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
						Username: dinkerlib.Def(args.FromCreds.User, ""),
						Password: dinkerlib.Def(args.FromCreds.Password, ""),
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
	destDirPath := dinkerlib.AbsPath(t0)
	defer func() {
		if err := os.RemoveAll(destDirPath.Raw()); err != nil {
			log.Printf("Error deleting temp image dir at %s: %s", destDirPath, err)
		}
	}()

	log.Printf("Building image...")
	if err := dinkerlib.BuildImage(dinkerlib.BuildImageArgs{
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
					Username: dinkerlib.Def(args.DestCreds.User, ""),
					Password: dinkerlib.Def(args.DestCreds.Password, ""),
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
