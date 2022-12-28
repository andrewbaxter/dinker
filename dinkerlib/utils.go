package dinkerlib

import (
	"os"
	"path/filepath"
)

func Def[T comparable](v T, alt T) T {
	var ref T
	if v == ref {
		return alt
	} else {
		return v
	}
}

type AbsPath string

func MakeAbsPath(relOrAbs string) AbsPath {
	p, err := filepath.Abs(relOrAbs)
	if err != nil {
		panic(err)
	}
	return AbsPath(p)
}

// During json unmarshaling, relative paths are based on the working directory of dinker
func (s *AbsPath) UnmarshalText(text []byte) error {
	*s = MakeAbsPath(string(text))
	return nil
}

func (p AbsPath) String() string {
	return string(p)
}

func (p AbsPath) Raw() string {
	return string(p)
}

func (p AbsPath) Parent() AbsPath {
	parent, _ := filepath.Split(p.Raw())
	return AbsPath(parent)
}

func (p AbsPath) Filename() string {
	return filepath.Base(string(p))
}

func (p AbsPath) Join(rel string) AbsPath {
	if filepath.IsAbs(rel) {
		panic("join path abs: " + rel)
	}
	return AbsPath(filepath.Clean(filepath.Join(string(p), rel)))
}

func (p *AbsPath) Exists() bool {
	_, err := os.Stat(p.Raw())
	return !os.IsNotExist(err)
}
