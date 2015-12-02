// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

// gb functional tests adapted from src/cmd/go/go_test.go

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var (
	canRun  = true  // whether we can run go or ./testgb
	canRace = false // whether we can run the race detector
	canCgo  = false // whether we can use cgo

	exeSuffix string // ".exe" on Windows

	skipExternalBuilder = false // skip external tests on this builder

	testgb string = "testgb"
)

func init() {
	switch runtime.GOOS {
	case "android", "nacl":
		canRun = false
	case "darwin":
		switch runtime.GOARCH {
		case "arm", "arm64":
			canRun = false
		}
	}

	switch runtime.GOOS {
	case "windows":
		exeSuffix = ".exe"
	}
	testgb += exeSuffix
}

// The TestMain function creates a gb command for testing purposes and
// deletes it after the tests have been run.
func TestMain(m *testing.M) {
	flag.Parse()

	if canRun {
		dir, err := ioutil.TempDir("", "testgb")
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot create temporary directory: %v", err)
			os.Exit(2)
		}
		testgb = filepath.Join(dir, testgb)
		out, err := exec.Command("go", "build", "-o", testgb).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "building testgb failed: %v\n%s", err, out)
			os.Exit(2)
		}

		switch runtime.GOOS {
		case "linux", "darwin", "freebsd", "windows":
			canRace = canCgo && runtime.GOARCH == "amd64"
		}
		defer os.RemoveAll(dir)
	}

	// Don't let these environment variables confuse the test.
	os.Unsetenv("GOBIN")
	os.Unsetenv("GOPATH")

	r := m.Run()
	os.Exit(r)
}

// T manage a single run of the testgb binary.
type T struct {
	*testing.T
	temps          []string
	wd             string
	env            []string
	tempdir        string
	ran            bool
	stdout, stderr bytes.Buffer
}

// must gives a fatal error if err is not nil.
func (t *T) must(err error) {
	if err != nil {
		t.Fatal(err)
	}
}

// check gives a test non-fatal error if err is not nil.
func (t *T) check(err error) {
	if err != nil {
		t.Error(err)
	}
}

// pwd returns the current directory.
func (t *T) pwd() string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	return wd
}

// cd changes the current directory to the named directory. extra args
// are passed through filepath.Join before cd.
func (t *T) cd(dir string, extra ...string) {
	if t.wd == "" {
		t.wd = t.pwd()
	}
	v := append([]string{dir}, extra...)
	dir = filepath.Join(v...)
	abs, err := filepath.Abs(dir)
	t.must(os.Chdir(dir))
	if err == nil {
		t.setenv("PWD", abs)
	}
}

// setenv sets an environment variable to use when running the test go
// command.
func (t *T) setenv(name, val string) {
	t.unsetenv(name)
	t.env = append(t.env, name+"="+val)
}

// unsetenv removes an environment variable.
func (t *T) unsetenv(name string) {
	if t.env == nil {
		t.env = append([]string(nil), os.Environ()...)
	}
	for i, v := range t.env {
		if strings.HasPrefix(v, name+"=") {
			t.env = append(t.env[:i], t.env[i+1:]...)
			break
		}
	}
}

// doRun runs the test go command, recording stdout and stderr and
// returning exit status.
func (t *T) doRun(args []string) error {
	if !canRun {
		t.Fatal("T.doRun called but canRun false")
	}
	t.Logf("running %v %v", testgb, args)
	cmd := exec.Command(testgb, args...)
	t.stdout.Reset()
	t.stderr.Reset()
	cmd.Stdout = &t.stdout
	cmd.Stderr = &t.stderr
	cmd.Env = t.env
	status := cmd.Run()
	if t.stdout.Len() > 0 {
		t.Log("standard output:")
		t.Log(t.stdout.String())
	}
	if t.stderr.Len() > 0 {
		t.Log("standard error:")
		t.Log(t.stderr.String())
	}
	t.ran = true
	return status
}

// run runs the test go command, and expects it to succeed.
func (t *T) run(args ...string) {
	if status := t.doRun(args); status != nil {
		t.Logf("gb %v failed unexpectedly: %v", args, status)
		t.FailNow()
	}
}

// runFail runs the test go command, and expects it to fail.
func (t *T) runFail(args ...string) {
	if status := t.doRun(args); status == nil {
		t.Fatal(testgb, "succeeded unexpectedly")
	} else {
		t.Log(testgb, "failed as expected:", status)
	}
}

// getStdout returns standard output of the testgb run as a string.
func (t *T) getStdout() string {
	if !t.ran {
		t.Fatal("internal testsuite error: stdout called before run")
	}
	return t.stdout.String()
}

// getStderr returns standard error of the testgb run as a string.
func (t *T) getStderr() string {
	if !t.ran {
		t.Fatal("internal testsuite error: stdout called before run")
	}
	return t.stderr.String()
}

// doGrepMatch looks for a regular expression in a buffer, and returns
// whether it is found.  The regular expression is matched against
// each line separately, as with the grep command.
func (t *T) doGrepMatch(match string, b *bytes.Buffer) bool {
	if !t.ran {
		t.Fatal("internal testsuite error: grep called before run")
	}
	re := regexp.MustCompile(match)
	for _, ln := range bytes.Split(b.Bytes(), []byte{'\n'}) {
		if re.Match(ln) {
			return true
		}
	}
	return false
}

// doGrep looks for a regular expression in a buffer and fails if it
// is not found.  The name argument is the name of the output we are
// searching, "output" or "error".  The msg argument is logged on
// failure.
func (t *T) doGrep(match string, b *bytes.Buffer, name, msg string) {
	if !t.doGrepMatch(match, b) {
		t.Log(msg)
		t.Logf("pattern %v not found in standard %s", match, name)
		t.FailNow()
	}
}

// grepStdout looks for a regular expression in the test run's
// standard output and fails, logging msg, if it is not found.
func (t *T) grepStdout(match, msg string) {
	t.doGrep(match, &t.stdout, "output", msg)
}

// grepStderr looks for a regular expression in the test run's
// standard error and fails, logging msg, if it is not found.
func (t *T) grepStderr(match, msg string) {
	t.doGrep(match, &t.stderr, "error", msg)
}

// grepBoth looks for a regular expression in the test run's standard
// output or stand error and fails, logging msg, if it is not found.
func (t *T) grepBoth(match, msg string) {
	if !t.doGrepMatch(match, &t.stdout) && !t.doGrepMatch(match, &t.stderr) {
		t.Log(msg)
		t.Logf("pattern %v not found in standard output or standard error", match)
		t.FailNow()
	}
}

// doGrepNot looks for a regular expression in a buffer and fails if
// it is found.  The name and msg arguments are as for doGrep.
func (t *T) doGrepNot(match string, b *bytes.Buffer, name, msg string) {
	if t.doGrepMatch(match, b) {
		t.Log(msg)
		t.Logf("pattern %v found unexpectedly in standard %s", match, name)
		t.FailNow()
	}
}

// grepStdoutNot looks for a regular expression in the test run's
// standard output and fails, logging msg, if it is found.
func (t *T) grepStdoutNot(match, msg string) {
	t.doGrepNot(match, &t.stdout, "output", msg)
}

// grepStderrNot looks for a regular expression in the test run's
// standard error and fails, logging msg, if it is found.
func (t *T) grepStderrNot(match, msg string) {
	t.doGrepNot(match, &t.stderr, "error", msg)
}

// grepBothNot looks for a regular expression in the test run's
// standard output or stand error and fails, logging msg, if it is
// found.
func (t *T) grepBothNot(match, msg string) {
	if t.doGrepMatch(match, &t.stdout) || t.doGrepMatch(match, &t.stderr) {
		t.Log(msg)
		t.Fatalf("pattern %v found unexpectedly in standard output or standard error", match)
	}
}

// doGrepCount counts the number of times a regexp is seen in a buffer.
func (t *T) doGrepCount(match string, b *bytes.Buffer) int {
	if !t.ran {
		t.Fatal("internal testsuite error: doGrepCount called before run")
	}
	re := regexp.MustCompile(match)
	c := 0
	for _, ln := range bytes.Split(b.Bytes(), []byte{'\n'}) {
		if re.Match(ln) {
			c++
		}
	}
	return c
}

// grepCountStdout returns the number of times a regexp is seen in
// standard output.
func (t *T) grepCountStdout(match string) int {
	return t.doGrepCount(match, &t.stdout)
}

// grepCountStderr returns the number of times a regexp is seen in
// standard error.
func (t *T) grepCountStderr(match string) int {
	return t.doGrepCount(match, &t.stderr)
}

// grepCountBoth returns the number of times a regexp is seen in both
// standard output and standard error.
func (t *T) grepCountBoth(match string) int {
	return t.doGrepCount(match, &t.stdout) + t.doGrepCount(match, &t.stderr)
}

// creatingTemp records that the test plans to create a temporary file
// or directory.  If the file or directory exists already, it will be
// removed.  When the test completes, the file or directory will be
// removed if it exists.
func (t *T) creatingTemp(path string) {
	if filepath.IsAbs(path) && !strings.HasPrefix(path, t.tempdir) {
		t.Fatal("internal testsuite error: creatingTemp(%q) with absolute path not in temporary directory", path)
	}
	// If we have changed the working directory, make sure we have
	// an absolute path, because we are going to change directory
	// back before we remove the temporary.
	if t.wd != "" && !filepath.IsAbs(path) {
		path = filepath.Join(t.pwd(), path)
	}
	t.must(os.RemoveAll(path))
	t.temps = append(t.temps, path)
}

// makeTempdir makes a temporary directory for a run of testgb.  If
// the temporary directory was already created, this does nothing.
func (t *T) makeTempdir() {
	if t.tempdir == "" {
		var err error
		t.tempdir, err = ioutil.TempDir("", "testgb")
		t.must(err)
		t.tempdir, err = filepath.EvalSymlinks(t.tempdir) // resolve OSX's stupid symlinked /tmp
		t.must(err)
	}
}

// tempFile adds a temporary file for a run of testgb.
func (t *T) tempFile(path, contents string) {
	t.makeTempdir()
	t.must(os.MkdirAll(filepath.Join(t.tempdir, filepath.Dir(path)), 0755))
	bytes := []byte(contents)
	if strings.HasSuffix(path, ".go") {
		formatted, err := format.Source(bytes)
		if err == nil {
			bytes = formatted
		}
	}
	t.must(ioutil.WriteFile(filepath.Join(t.tempdir, path), bytes, 0644))
}

// tempDir adds a temporary directory for a run of testgb.
func (t *T) tempDir(path string) string {
	t.makeTempdir()
	path = filepath.Join(t.tempdir, path)
	if err := os.MkdirAll(path, 0755); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	return path
}

// path returns the absolute pathname to file with the temporary
// directory.
func (t *T) path(name string) string {
	if t.tempdir == "" {
		t.Fatalf("internal testsuite error: path(%q) with no tempdir", name)
	}
	if name == "." {
		return t.tempdir
	}
	return filepath.Join(t.tempdir, name)
}

// mustNotExist fails if path exists.
func (t *T) mustNotExist(path string) {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		t.Fatalf("%s exists but should not (%v)", path, err)
	}
}

// mustBeEmpty fails if root is not a directory or is not empty.
func (t *T) mustBeEmpty(root string) {
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("failed to stat: %s: %v", root, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s exists but is not a directory", root)
	}
	var found []string
	fn := func(path string, info os.FileInfo, err error) error {
		if path == root {
			return nil
		}
		if err != nil {
			t.Fatalf("error during walk at %s: %v", path, err)
		}
		found = append(found, path)
		return nil
	}
	err = filepath.Walk(root, fn)
	if len(found) > 0 {
		t.Fatalf("expected %s to be empty, found %s", root, found)
	}
}

// wantExecutable fails with msg if path is not executable.
func (t *T) wantExecutable(path, msg string) {
	if st, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			t.Log(err)
		}
		t.Fatal(msg)
	} else {
		if runtime.GOOS != "windows" && st.Mode()&0111 == 0 {
			t.Fatalf("binary %s exists but is not executable", path)
		}
	}
}

// wantArchive fails if path is not an archive.
func (t *T) wantArchive(path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	io.ReadFull(f, buf)
	f.Close()
	if !bytes.HasPrefix(buf, []byte("!<arch>\n")) {
		t.Fatalf("file %s exists but is not an archive", path)
	}
}

// isStale returns whether pkg is stale.
func (t *T) isStale(pkg string) bool {
	t.run("list", "-f", "{{.Stale}}", pkg)
	switch v := strings.TrimSpace(t.getStdout()); v {
	case "true":
		return true
	case "false":
		return false
	default:
		t.Fatalf("unexpected output checking staleness of package %v: %v", pkg, v)
		panic("unreachable")
	}
}

// wantStale fails with msg if pkg is not stale.
func (t *T) wantStale(pkg, msg string) {
	if !t.isStale(pkg) {
		t.Fatal(msg)
	}
}

// wantNotStale fails with msg if pkg is stale.
func (t *T) wantNotStale(pkg, msg string) {
	if t.isStale(pkg) {
		t.Fatal(msg)
	}
}

// cleanup cleans up a test that runs testgb.
func (t *T) cleanup() {
	if t.wd != "" {
		if err := os.Chdir(t.wd); err != nil {
			// We are unlikely to be able to continue.
			fmt.Fprintln(os.Stderr, "could not restore working directory, crashing:", err)
			os.Exit(2)
		}
	}
	for _, path := range t.temps {
		t.check(os.RemoveAll(path))
	}
	if t.tempdir != "" {
		t.check(os.RemoveAll(t.tempdir))
	}
}

// resetReadOnlyFlagAll resets windows read-only flag
// set on path and any children it contains.
// The flag is set by git and has to be removed.
// os.Remove refuses to remove files with read-only flag set.
func (t *T) resetReadOnlyFlagAll(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("resetReadOnlyFlagAll(%q) failed: %v", path, err)
	}
	if !fi.IsDir() {
		err := os.Chmod(path, 0666)
		if err != nil {
			t.Fatalf("resetReadOnlyFlagAll(%q) failed: %v", path, err)
		}
	}
	fd, err := os.Open(path)
	if err != nil {
		t.Fatalf("resetReadOnlyFlagAll(%q) failed: %v", path, err)
	}
	defer fd.Close()
	names, _ := fd.Readdirnames(-1)
	for _, name := range names {
		t.resetReadOnlyFlagAll(path + string(filepath.Separator) + name)
	}
}

// Invoking plain "gb" should print usage to stderr and exit with 2.
func TestNoArguments(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()

	gb.tempDir("src")
	gb.cd(gb.tempdir)
	gb.runFail()
	gb.grepStderr("^Usage:", `expected "Usage: ..."`)
}

// Invoking plain "gb" outside a project should print to stderr and exit with 2.
func TestOutsideProject(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()

	gb.tempDir("x")
	gb.cd(gb.tempdir, "x")
	gb.runFail()
	gb.grepStderr("^Usage:", `expected "Usage: ..."`)
}

// Invoking gb outside a project should print to stderr and exit with 2.
func TestInfoOutsideProject(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()

	gb.tempDir("x")
	gb.cd(gb.tempdir, "x")
	gb.runFail("info")
	regex := `FATAL: unable to construct context: could not locate project root: could not find project root in "` +
		regexp.QuoteMeta(filepath.Join(gb.tempdir, "x")) +
		`" or its parents`
	gb.grepStderr(regex, "expected FATAL")
}

// Invoking gb outside a project with -R should succeed.
func TestInfoWithMinusR(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()

	gb.tempDir("x")
	gb.tempDir("y")
	gb.tempDir("y/src")
	gb.cd(gb.tempdir, "x")
	gb.run("info", "-R", filepath.Join(gb.tempdir, "y"))
	gb.grepStdout(`^GB_PROJECT_DIR="`+regexp.QuoteMeta(filepath.Join(gb.tempdir, "y"))+`"$`, "missing GB_PROJECT_DIR")
}

func TestInfoCmd(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()

	gb.tempDir("src")
	gb.cd(gb.tempdir)
	gb.run("info")
	gb.grepStdout(`^GB_PROJECT_DIR="`+regexp.QuoteMeta(gb.tempdir)+`"$`, "missing GB_PROJECT_DIR")
	gb.grepStdout(`^GB_SRC_PATH="`+regexp.QuoteMeta(filepath.Join(gb.tempdir, "src")+string(filepath.ListSeparator)+filepath.Join(gb.tempdir, "vendor", "src"))+`"$`, "missing GB_SRC_PATH")
	gb.grepStdout(`^GB_PKG_DIR="`+regexp.QuoteMeta(filepath.Join(gb.tempdir, "pkg", runtime.GOOS+"-"+runtime.GOARCH))+`"$`, "missing GB_PKG_DIR")
	gb.grepStdout(`^GB_BIN_SUFFIX="-`+runtime.GOOS+"-"+runtime.GOARCH+`"$`, "missing GB_BIN_SUFFIX")
	gb.grepStdout(`^GB_GOROOT="`+regexp.QuoteMeta(runtime.GOROOT())+`"$`, "missing GB_GOROOT")
}

// Only succeeds if source order is preserved.
func TestSourceFileNameOrderPreserved(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/testorder")
	gb.tempFile("src/testorder/example1_test.go", `// Copyright 2013 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Make sure that go test runs Example_Z before Example_A, preserving source order.

package p

import "fmt"

var n int

func Example_Z() {
	n++
	fmt.Println(n)
	// Output: 1
}

func Example_A() {
	n++
	fmt.Println(n)
	// Output: 2
}
`)
	gb.tempFile("src/testorder/example2_test.go", `// Copyright 2013 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Make sure that go test runs Example_Y before Example_B, preserving source order.

package p

import "fmt"

func Example_Y() {
	n++
	fmt.Println(n)
	// Output: 3
}

func Example_B() {
	n++
	fmt.Println(n)
	// Output: 4
}
`)
	gb.cd(gb.tempdir)
	gb.run("test", "testorder")
}

func TestBuildPackage(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg.go", `package pkg1
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("build")
	gb.grepStdout("^pkg1$", `expected "pkg1"`)
	gb.mustBeEmpty(tmpdir)
	gb.wantArchive(filepath.Join(gb.tempdir, "pkg", runtime.GOOS+"-"+runtime.GOARCH, "pkg1.a"))
}

func TestBuildOnlyOnePackage(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg.go", `package pkg1
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.tempDir("src/pkg2")
	gb.tempFile("src/pkg2/pkg.go", `package pkg2
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("build", "pkg1")
	gb.grepStdout("^pkg1$", `expected "pkg1"`)
	gb.grepStdoutNot("^pkg2$", `did not expect "pkg2"`)
	gb.mustBeEmpty(tmpdir)
	gb.wantArchive(filepath.Join(gb.tempdir, "pkg", runtime.GOOS+"-"+runtime.GOARCH, "pkg1.a"))
}

func TestBuildOnlyOnePackageFromWorkingDir(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg.go", `package pkg1
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.tempDir("src/pkg2")
	gb.tempFile("src/pkg2/pkg.go", `package pkg2
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.cd(filepath.Join(gb.tempdir, "src", "pkg1"))
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("build")
	gb.grepStdout("^pkg1$", `expected "pkg1"`)
	gb.grepStdoutNot("^pkg2$", `did not expect "pkg2"`)
	gb.mustBeEmpty(tmpdir)
	gb.wantArchive(filepath.Join(gb.tempdir, "pkg", runtime.GOOS+"-"+runtime.GOARCH, "pkg1.a"))
}

func TestBuildPackageWrongPackage(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg.go", `package pkg1
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.cd(gb.tempdir)
	gb.runFail("build", "pkg2")
	gb.grepStderr(`^FATAL: command "build" failed: failed to resolve import path "pkg2": cannot find package "pkg2" in any of:`, "expected FATAL")
	gb.grepStderr(regexp.QuoteMeta(filepath.Join(runtime.GOROOT(), "src", "pkg2")), "expected GOROOT")
	gb.grepStderr(regexp.QuoteMeta(filepath.Join(gb.tempdir, "src", "pkg2")), "expected GOPATH")
	gb.grepStderr(regexp.QuoteMeta(filepath.Join(gb.tempdir, "vendor", "src", "pkg2")), "expected GOPATH")
}

func TestBuildPackageNoSource(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.cd(gb.tempdir)
	gb.runFail("build", "pkg1")
	gb.grepStderr(`^FATAL: command "build" failed: failed to resolve import path "pkg1": no buildable Go source files in `+regexp.QuoteMeta(filepath.Join(gb.tempdir, "src", "pkg1")), "expected FATAL")
}

func TestTestPackageNoTests(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg.go", `package pkg1
import "fmt"

func helloworld() {
	fmt.Println("hello world!")
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("test", "pkg1")
	gb.grepStdout("^pkg1$", `expected "pkg1"`)
	gb.mustBeEmpty(tmpdir)
	gb.mustNotExist(filepath.Join(gb.tempdir, "pkg")) // ensure no pkg directory is created
}

// test that compiling A in test scope compiles B in regular scope
func TestTestDepdenantPackage(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/A")
	gb.tempDir("src/B")
	gb.tempFile("src/B/B.go", `package B
const X = 1
`)
	gb.tempFile("src/A/A_test.go", `package A
import "testing"
import "B"

func TestX(t *testing.T) {
	if B.X != 1 {
		t.Fatal("expected 1, got %d", B.X)
	}
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("test", "A")
	gb.grepStdout("^B$", `expected "B"`) // output from build action
	gb.grepStdout("^A$", `expected "A"`) // output from test action
	gb.mustBeEmpty(tmpdir)
	gb.wantArchive(filepath.Join(gb.tempdir, "pkg", runtime.GOOS+"-"+runtime.GOARCH, "B.a"))
}

func TestTestPackageOnlyTests(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg_test.go", `package pkg1
import "testing"

func TestTest(t *testing.T) {
	t.Log("hello")
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.run("test", "pkg1")
	gb.grepStdout("^pkg1$", `expected "pkg1"`)
	gb.mustBeEmpty(tmpdir)
	gb.mustNotExist(filepath.Join(gb.tempdir, "pkg")) // ensure no pkg directory is created
}

func TestTestPackageFailedToBuild(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg_test.go", `package pkg1
import "testing"

func TestTest(t *testing.T) {
	t.Log("hello"	// missing parens
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.runFail("test")
	gb.grepStderr(`FATAL: command "test" failed:`, "expected FATAL")
	gb.mustBeEmpty(tmpdir)
	gb.mustNotExist(filepath.Join(gb.tempdir, "pkg")) // ensure no pkg directory is created
}

func TestTestPackageTestFailed(t *testing.T) {
	gb := T{T: t}
	defer gb.cleanup()
	gb.tempDir("src")
	gb.tempDir("src/pkg1")
	gb.tempFile("src/pkg1/pkg_test.go", `package pkg1
import "testing"

func TestTest(t *testing.T) {
	t.Error("failed")
}
`)
	gb.cd(gb.tempdir)
	tmpdir := gb.tempDir("tmp")
	gb.setenv("TMP", tmpdir)
	gb.runFail("test")
	gb.grepStderr("^# pkg1$", "expected # pkg1")
	gb.grepStdout("pkg_test.go:6: failed", "expected message from test")
	gb.mustBeEmpty(tmpdir)
	gb.mustNotExist(filepath.Join(gb.tempdir, "pkg")) // ensure no pkg directory is created
}
