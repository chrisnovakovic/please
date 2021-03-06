package remote

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/ptypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thought-machine/please/src/fs"

	"github.com/thought-machine/please/src/core"
)

func TestInit(t *testing.T) {
	c := newClient()
	assert.NoError(t, c.CheckInitialised())
}

func TestBadAPIVersion(t *testing.T) {
	// We specify a required API version of v2.0.0, so should fail initialisation if the server
	// specifies something incompatible with that.
	defer server.Reset()
	server.HighAPIVersion.Major = 1
	server.LowAPIVersion.Major = 1
	c := newClient()
	assert.Error(t, c.CheckInitialised())
	assert.Contains(t, c.CheckInitialised().Error(), "1.0.0 - 1.1.0")
}

func TestUnsupportedDigest(t *testing.T) {
	defer server.Reset()
	server.DigestFunction = []pb.DigestFunction_Value{
		pb.DigestFunction_MD5,
		pb.DigestFunction_SHA384,
		pb.DigestFunction_SHA512,
	}
	c := newClient()
	assert.Error(t, c.CheckInitialised())
}

const xmlResults = `<?xml version="1.0" encoding="UTF-8"?>
<testsuite errors="0" failures="0" name="src.build.python.pex_test.PexTest-20150416153858" tests="1" time="0.000">
<properties/>
<testcase classname="src.build.python.pex_test.PexTest" name="testSuccess" time="0.000"/>           <testcase classname="src.build.python.pex_test.PexTest" name="testSuccess" time="0.000">
        </testcase>
</testsuite>
`

func TestExecuteBuild(t *testing.T) {
	c := newClient()
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target2"})
	target.AddSource(core.FileLabel{File: "src1.txt", Package: "package"})
	target.AddSource(core.FileLabel{File: "src2.txt", Package: "package"})
	target.AddOutput("out2.txt")
	target.BuildTimeout = time.Minute
	// We need to set this to force stdout to be retrieved (it is otherwise unnecessary
	// on success).
	target.PostBuildFunction = testFunction{}
	target.Command = "echo hello && echo test > $OUT"
	metadata, err := c.Build(0, target)
	assert.NoError(t, err)
	assert.Equal(t, []byte("hello\n"), metadata.Stdout)
}

type postBuildFunction func(*core.BuildTarget, string) error

func (f postBuildFunction) Call(target *core.BuildTarget, output string) error {
	return f(target, output)
}
func (f postBuildFunction) String() string { return "" }

func TestExecutePostBuildFunction(t *testing.T) {
	t.Skip("Post-build function currently triggered at a higher level")
	c := newClient()
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target5"})
	target.BuildTimeout = time.Minute
	target.Command = "echo 'wibble wibble wibble' | tee file7"
	target.PostBuildFunction = postBuildFunction(func(target *core.BuildTarget, output string) error {
		target.AddOutput("somefile")
		assert.Equal(t, "wibble wibble wibble", output)
		return nil
	})
	_, err := c.Build(0, target)
	assert.NoError(t, err)
	assert.Equal(t, []string{"somefile"}, target.Outputs())
}

func TestExecuteFetch(t *testing.T) {
	c := newClient()
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "remote1"})
	target.IsRemoteFile = true
	target.AddSource(core.URLLabel("https://get.please.build/linux_amd64/14.2.0/please_14.2.0.tar.gz"))
	target.AddOutput("please_14.2.0.tar.gz")
	target.Hashes = []string{"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
	target.BuildTimeout = time.Minute
	_, err := c.Build(0, target)
	assert.NoError(t, err)
}

func TestExecuteTest(t *testing.T) {
	c := newClientInstance("test")
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target3"})
	target.AddOutput("remote_test")
	target.TestTimeout = time.Minute
	target.TestCommand = "$TEST"
	target.IsTest = true
	target.IsBinary = true
	target.SetState(core.Building)
	err := c.Store(target)
	assert.NoError(t, err)
	c.state.Graph.AddTarget(target)
	_, results, coverage, err := c.Test(0, target)
	assert.NoError(t, err)
	assert.Equal(t, testResults, results)
	assert.Equal(t, 0, len(coverage)) // Wasn't requested
}

func TestExecuteTestWithCoverage(t *testing.T) {
	c := newClientInstance("test")
	c.state.NeedCoverage = true // bit of a hack but we need to turn this on somehow
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target4"})
	target.AddOutput("remote_test")
	target.TestTimeout = time.Minute
	target.TestCommand = "$TEST"
	target.IsTest = true
	target.IsBinary = true
	err := c.Store(target)
	assert.NoError(t, err)
	target.SetState(core.Built)
	c.state.Graph.AddTarget(target)
	_, results, coverage, err := c.Test(0, target)
	assert.NoError(t, err)
	assert.Equal(t, testResults, results)
	assert.Equal(t, coverageData, coverage)
}

var testResults = [][]byte{[]byte(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<testcase name="//src/remote:remote_test">
  <test name="testResults" success="true" time="172" type="SUCCESS"/>
</testcase>
`)}

var coverageData = []byte(`mode: set
src/core/build_target.go:134.54,143.2 7 0
src/core/build_target.go:159.52,172.2 12 0
src/core/build_target.go:177.44,179.2 1 0
`)

func TestNoAbsolutePaths(t *testing.T) {
	c := newClientInstance("test")
	tool := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "tool"})
	tool.AddOutput("bin")
	c.state.Graph.AddTarget(tool)
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target5"})
	target.AddOutput("remote_test")
	target.AddSource(core.FileLabel{Package: "package", File: "file"})
	target.AddTool(tool.Label)
	cmd, _ := c.buildCommand(target, &pb.Directory{}, false, false, false)
	testDir := os.Getenv("TEST_DIR")
	for _, env := range cmd.EnvironmentVariables {
		if !strings.HasPrefix(env.Value, "//") {
			assert.False(t, path.IsAbs(env.Value), "Env var %s has an absolute path: %s", env.Name, env.Value)
			assert.NotContains(t, env.Value, core.OutDir, "Env var %s contains %s: %s", env.Name, core.OutDir, env.Value)
			assert.NotContains(t, env.Value, testDir, "Env var %s contains the test dir %s: %s", env.Name, testDir, env.Value)
		}
	}
}

func TestNoAbsolutePaths2(t *testing.T) {
	c := newClientInstance("test")
	tool := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "tool"})
	tool.AddOutput("bin")
	c.state.Graph.AddTarget(tool)
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "target5"})
	target.AddOutput("remote_test")
	target.AddTool(core.SystemPathLabel{Path: []string{os.Getenv("TMP_DIR")}, Name: "remote_test"})
	cmd, _ := c.buildCommand(target, &pb.Directory{}, false, false, false)
	for _, env := range cmd.EnvironmentVariables {
		if !strings.HasPrefix(env.Value, "//") {
			assert.False(t, path.IsAbs(env.Value), "Env var %s has an absolute path: %s", env.Name, env.Value)
			assert.NotContains(t, env.Value, core.OutDir, "Env var %s contains %s: %s", env.Name, core.OutDir, env.Value)
		}
	}
}

func TestRemoteFilesHashConsistently(t *testing.T) {
	c := newClientInstance("test")
	target := core.NewBuildTarget(core.BuildLabel{PackageName: "package", Name: "download"})
	target.IsRemoteFile = true
	target.AddSource(core.URLLabel("https://localhost/file"))
	cmd, digest, err := c.buildAction(target, false, false)
	assert.NoError(t, err)
	// After we change this path, the rule should still give back the same protos since it is
	// not relevant to how we fetch a remote asset.
	c.state.Config.Build.Path = []string{"/usr/bin/nope"}
	cmd2, digest2, err := c.buildAction(target, false, false)
	assert.NoError(t, err)
	assert.Equal(t, cmd, cmd2)
	assert.Equal(t, digest, digest2)
}

func TestOutDirsSetOutsOnTarget(t *testing.T) {
	c := newClientInstance("mock")

	foo := []byte("this is the content of foo")
	fooDigest := digest.NewFromBlob(foo)

	bar := []byte("this is the content of bar")
	barDigest := digest.NewFromBlob(bar)

	tree := mustMarshal(&pb.Tree{
		Root: &pb.Directory{
			Files: []*pb.FileNode{
				{Name: "foo.txt", Digest: fooDigest.ToProto()},
				{Name: "bar.txt", Digest: barDigest.ToProto()},
			},
		},
	})
	treeDigest := digest.NewFromBlob(tree)

	server.mockActionResult = &pb.ActionResult{
		OutputDirectories: []*pb.OutputDirectory{
			{
				Path:       "foo",
				TreeDigest: treeDigest.ToProto(),
			},
		},
		ExitCode: 0,
		ExecutionMetadata: &pb.ExecutedActionMetadata{
			Worker:                      "kev",
			QueuedTimestamp:             ptypes.TimestampNow(),
			ExecutionStartTimestamp:     ptypes.TimestampNow(),
			ExecutionCompletedTimestamp: ptypes.TimestampNow(),
		},
	}

	server.blobs[treeDigest.Hash] = tree

	server.blobs[fooDigest.Hash] = foo
	server.blobs[barDigest.Hash] = bar

	outDirTarget := core.NewBuildTarget(core.BuildLabel{
		PackageName: "package",
		Name:        "out_dir_target",
	})

	c.state.OriginalTargets = append(c.state.OriginalTargets, outDirTarget.Label)
	c.state.DownloadOutputs = true
	require.True(t, c.state.ShouldDownload(outDirTarget))

	outDirTarget.AddOutputDirectory("foo")
	// Doesn't actually get executed but gives an idea as to how this rule is mocked up
	outDirTarget.Command = "touch foo/bar.txt && touch foo/baz.txt"
	c.state.Graph.AddTarget(outDirTarget)
	_, err := c.Build(0, outDirTarget)
	require.NoError(t, err)

	assert.Len(t, outDirTarget.Outputs(), 2)
	assert.ElementsMatch(t, []string{"foo.txt", "bar.txt"}, outDirTarget.Outputs())
	for _, out := range outDirTarget.Outputs() {
		assert.True(t, fs.FileExists(filepath.Join(outDirTarget.OutDir(), out)), "output %s doesn't exist in target out folder", out)
	}
}

// Store is a small hack that stores a target's outputs for testing only.
func (c *Client) Store(target *core.BuildTarget) error {
	if err := c.CheckInitialised(); err != nil {
		return err
	}
	return c.uploadLocalTarget(target)
}
