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
	// `tcp` or `udp`
	Transport string `json:"transport"`
}

type BuildImageArgs struct {
	FromPath AbsPath
	Files    []BuildImageArgsFile
	ClearEnv bool
	AddEnv   map[string]string
	// Defaults to FROM image working dir
	WorkingDir string
	// Defaults to FROM image user
	User        string
	Entrypoint  []string
	Cmd         []string
	Ports       []BuildImageArgsPort `json:"ports"`
	StopSignal  string
	Labels      map[string]string
	DestDirPath AbsPath
}
