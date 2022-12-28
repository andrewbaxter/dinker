package dinkerlib

import (
	"os"
	"path/filepath"
)

func Def[T any](v *T, alt T) T {
	if v == nil {
		return alt
	} else {
		return *v
	}
}

type AbsPath string

// During json unmarshaling, relative paths are based on the working directory of dinker
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

func (p *AbsPath) Parent() *AbsPath {
	parent, _ := filepath.Split(p.Raw())
	out := AbsPath(parent)
	return &out
}

func (p *AbsPath) Filename() string {
	return filepath.Base(string(*p))
}

func (p *AbsPath) Join(rel string) *AbsPath {
	if filepath.IsAbs(rel) {
		panic("join path abs: " + rel)
	}
	out := AbsPath(filepath.Clean(filepath.Join(string(*p), rel)))
	return &out
}

func (p *AbsPath) Exists() bool {
	_, err := os.Stat(p.Raw())
	return !os.IsNotExist(err)
}
