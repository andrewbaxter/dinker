package dinkerlib

type BuildImageArgsDir struct {
	// Name in parent in destination tree
	Name string `json:"name"`
	// Parsed as octal, defaults to 0755
	Mode string `json:"mode"`
	// Child dirs
	Dirs []BuildImageArgsDir
	// Child files
	Files []BuildImageArgsFile
}

type BuildImageArgsFile struct {
	// Name in parent in destination tree. Defaults to filename of source if empty.
	Name string `json:"name"`
	// Path of file to copy from
	Source AbsPath `json:"source"`
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
	// Directories to build in the image root
	Dirs []BuildImageArgsDir
	// Files to add to the image root
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
