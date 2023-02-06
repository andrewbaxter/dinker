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
		return fmt.Errorf("invalid dest image ref %s: %w", args.Dest, err)
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
		var noHttpVerify types.OptionalBool
		if args.FromHttp {
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
						Username: args.FromUser,
						Password: args.FromPassword,
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
	var noHttpVerify types.OptionalBool
	if args.DestHttp {
		noHttpVerify = types.OptionalBoolTrue
	}
	destSysCtx := types.SystemContext{
		DockerInsecureSkipTLSVerify: noHttpVerify,
		DockerAuthConfig: &types.DockerAuthConfig{
			Username: args.DestUser,
			Password: args.DestPassword,
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
	log.Printf("Pushing to %s... done.", args.Dest)
	return nil
}

func main() {
	err := main0()
	if err != nil {
		log.Fatalf("Exiting with fatal error: %s", err)
	}
}
