package lua

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/* load lib {{{ */

var loLoaders = []LGFunction{loLoaderPreload, loLoaderLua}

func loGetPath(env string, defpath string) string {
	path := os.Getenv(env)
	if len(path) == 0 {
		return defpath
	}

	if !strings.Contains(path, ";;") {
		return path
	}

	if os.PathSeparator == '/' {
		return strings.Replace(path, ";;", ";"+defpath+";", -1)
	}

	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic(err)
	}

	path = strings.Replace(path, ";;", ";"+defpath+";", -1)
	return strings.Replace(path, "!", dir, -1)
}

func loFindFile(L *LState, name, pname string) (string, string) {
	name = strings.Replace(name, ".", string(os.PathSeparator), -1)

	lv := L.GetField(L.GetField(L.Get(EnvironIndex), "package"), pname)
	path, ok := lv.(LString)
	if !ok {
		L.RaiseError("package.%s must be a string", pname)
	}

	var messages []string
	patterns := strings.Split(string(path), ";")

	for _, pattern := range patterns {
		luapath := strings.Replace(pattern, "?", name, -1)
		_, err := os.Stat(luapath)
		if err == nil {
			return luapath, ""
		}
		messages = append(messages, err.Error())
	}

	return "", strings.Join(messages, "\n\t")
}

func OpenPackage(L *LState) int {
	if L == nil {
		return 0
	}

	packagemod := L.RegisterModule(LoadLibName, loFuncs)
	if packagemod == nil {
		return 0
	}

	preload := L.NewTable()
	if preload == nil {
		return 0
	}
	L.SetField(packagemod, "preload", preload)

	loaders := L.CreateTable(len(loLoaders), 0)
	if loaders == nil {
		return 0
	}

	for i, loader := range loLoaders {
		if loader == nil {
			continue
		}
		L.RawSetInt(loaders, i+1, L.NewFunction(loader))
	}

	L.SetField(packagemod, "loaders", loaders)
	L.SetField(L.Get(RegistryIndex), "_LOADERS", loaders)

	loaded := L.NewTable()
	if loaded == nil {
		return 0
	}

	L.SetField(packagemod, "loaded", loaded)
	L.SetField(L.Get(RegistryIndex), "_LOADED", loaded)

	path := loGetPath(LuaPath, LuaPathDefault)
	if path == "" {
		path = LuaPathDefault
	}
	L.SetField(packagemod, "path", LString(path))
	L.SetField(packagemod, "cpath", emptyLString)

	config := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n",
		LuaDirSep,
		LuaPathSep,
		LuaPathMark,
		LuaExecDir,
		LuaIgMark)
	L.SetField(packagemod, "config", LString(config))

	L.Push(packagemod)
	return 1
}

var loFuncs = map[string]LGFunction{
	"loadlib": loLoadLib,
	"seeall":  loSeeAll,
}

func loLoaderPreload(L *LState) int {
	if L == nil {
		return 0
	}

	name := L.CheckString(1)
	if name == "" {
		return 0
	}

	preload := L.GetField(L.GetField(L.Get(EnvironIndex), "package"), "preload")
	if preload == nil {
		L.RaiseError("package.preload must be a table")
		return 0
	}

	if _, ok := preload.(*LTable); !ok {
		L.RaiseError("package.preload must be a table")
		return 0
	}

	lv := L.GetField(preload, name)
	if lv == LNil {
		L.Push(LString(fmt.Sprintf("no field package.preload['%s']", name)))
		return 1
	}

	L.Push(lv)
	return 1
}

func loLoaderLua(L *LState) int {
	if L == nil {
		return 0
	}

	name := L.CheckString(1)
	if name == "" {
		return 0
	}

	path, msg := loFindFile(L, name, "path")
	if path == "" {
		L.Push(LString(msg))
		return 1
	}

	fn, err := L.LoadFile(path)
	if err != nil {
		L.RaiseError(err.Error())
		return 0
	}

	L.Push(fn)
	return 1
}

func loLoadLib(L *LState) int {
	if L == nil {
		return 0
	}
	L.RaiseError("loadlib is not supported")
	return 0
}

func loSeeAll(L *LState) int {
	if L == nil {
		return 0
	}

	mod := L.CheckTable(1)
	if mod == nil {
		return 0
	}

	mt := L.GetMetatable(mod)
	if mt == LNil {
		mt = L.CreateTable(0, 1)
		if mt == nil {
			return 0
		}
		L.SetMetatable(mod, mt)
	}

	L.SetField(mt, "__index", L.Get(GlobalsIndex))
	return 0
}

/* }}} */

//
