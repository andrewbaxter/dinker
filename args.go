package main

import "github.com/andrewbaxter/dinker/dinkerlib"

type Args struct {
	// Path to FROM oci image archive tar file. If not present, pulls using FromPull and stores it here.
	From dinkerlib.AbsPath `json:"from"`
	// Pull FROM oci image from this ref if it doesn't exist locally (skopeo-style)
	FromPull string `json:"from_pull"`
	// Credentials to pull FROM if necessary
	FromUser     string `json:"from_user"`
	FromPassword string `json:"from_password"`
	// True if source is over http (disable tls validation)
	FromHttp bool `json:"from_http"`
	// Save image to ref (skopeo-style)
	Dest string `json:"dest"`
	// Credentials to push to dest if necessary
	DestUser     string `json:"dest_user"`
	DestPassword string `json:"dest_password"`
	// True if dest is over http (disable tls validation)
	DestHttp bool                           `json:"dest_http"`
	Files    []dinkerlib.BuildImageArgsFile `json:"files"`
	// Add additional default environment values
	AddEnv map[string]string `json:"add_env"`
	// Clear inherited environment from FROM image
	ClearEnv   bool                           `json:"clear_env"`
	WorkingDir string                         `json:"working_dir"`
	User       string                         `json:"user"`
	Cmd        []string                       `json:"cmd"`
	Ports      []dinkerlib.BuildImageArgsPort `json:"ports"`
	Labels     map[string]string              `json:"labels"`
	StopSignal string                         `json:"stop_signal"`
}
