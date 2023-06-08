package dinkerlib

type BuildImageArgsFile struct {
	Source AbsPath `json:"source"`
	// Defaults to the filename of Source in /. Ex: if source is `a/b/c` the resulting image will have the file at `/c`
	Dest string `json:"dest"`
	// Parsed as octal, defaults to 0644
	Mode string `json:"mode"`
}

type BuildImageArgsPort struct {
	Port int `json:"port"`
	// `tcp` or `udp`, defaults to `tcp`
	Transport string `json:"transport"`
}

type BuildImageArgs struct {
	// optional, if zero then "scratch" (no base layers, need Architecture and Os below)
	FromPath AbsPath
	// Defaults to FROM image architecture
	Architecture string
	// Defaults to FROM image os
	Os string
	// Files to add to the image
	Files []BuildImageArgsFile
	// Don't inherit env from FROM image
	ClearEnv bool
	AddEnv   map[string]string
	// Defaults to FROM image working dir
	WorkingDir string
	// Defaults to FROM image user
	User       string
	Entrypoint []string
	Cmd        []string
	Ports      []BuildImageArgsPort
	StopSignal string
	Labels     map[string]string
	/// Where to place the built image as an oci-dir
	DestDirPath AbsPath
}
