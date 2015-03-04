package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"testing"
)

var (
	repoName    = fmt.Sprintf("%v/dockercli/busybox", privateRegistryURL)
	digestRegex = regexp.MustCompile("Digest: ([^\n]+)")
)

func setupImage() (string, error) {
	containerName := "busyboxbydigest"

	c := exec.Command(dockerBinary, "run", "-d", "-e", "digest=1", "--name", containerName, "busybox")
	if _, err := runCommand(c); err != nil {
		return "", err
	}

	// tag the image to upload it to the private registry
	c = exec.Command(dockerBinary, "commit", containerName, repoName)
	if out, _, err := runCommandWithOutput(c); err != nil {
		return "", fmt.Errorf("image tagging failed: %s, %v", out, err)
	}
	defer deleteImages(repoName)

	// delete the container as we don't need it any more
	if err := deleteContainer(containerName); err != nil {
		return "", err
	}

	// push the image
	c = exec.Command(dockerBinary, "push", repoName)
	out, _, err := runCommandWithOutput(c)
	if err != nil {
		return "", fmt.Errorf("pushing the image to the private registry has failed: %s, %v", out, err)
	}

	// delete busybox and our local repo that we previously tagged
	//if err := deleteImages(repoName, "busybox"); err != nil {
	c = exec.Command(dockerBinary, "rmi", repoName, "busybox")
	if out, _, err := runCommandWithOutput(c); err != nil {
		return "", fmt.Errorf("error deleting images prior to real test: %s, %v", out, err)
	}

	// the push output includes "Digest: <digest>", so find that
	matches := digestRegex.FindStringSubmatch(out)
	if len(matches) != 2 {
		return "", fmt.Errorf("unable to parse digest from push output: %s", out)
	}
	pushDigest := matches[1]

	return pushDigest, nil
}

func TestPullByDigest(t *testing.T) {
	defer setupRegistry(t)()

	pushDigest, err := setupImage()
	if err != nil {
		t.Fatalf("error setting up image: %v", err)
	}

	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)
	pullCmd := exec.Command(dockerBinary, "pull", imageReference)
	out, _, err := runCommandWithOutput(pullCmd)
	if err != nil {
		t.Fatalf("error pulling by digest: %s, %v", out, err)
	}
	defer deleteImages(imageReference)

	// the pull output includes "Digest: <digest>", so find that
	matches := digestRegex.FindStringSubmatch(out)
	if len(matches) != 2 {
		t.Fatalf("unable to parse digest from pull output: %s", out)
	}
	pullDigest := matches[1]

	// make sure the pushed and pull digests match
	if pushDigest != pullDigest {
		t.Fatalf("push digest %q didn't match pull digest %q", pushDigest, pullDigest)
	}

	logDone("by_digest - pull by digest")
}

func TestCreateByDigest(t *testing.T) {
	defer setupRegistry(t)()

	pushDigest, err := setupImage()
	if err != nil {
		t.Fatalf("error setting up image: %v", err)
	}

	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)

	containerName := "createByDigest"
	c := exec.Command(dockerBinary, "create", "--name", containerName, imageReference)
	out, _, err := runCommandWithOutput(c)
	if err != nil {
		t.Fatalf("error creating by digest: %s, %v", out, err)
	}
	defer deleteContainer(containerName)

	c = exec.Command(dockerBinary, "inspect", "-f", "{{.Config.Image}}", containerName)
	out, _, err = runCommandWithOutput(c)
	if err != nil {
		t.Fatalf("failed to get Config.Image: %s, %v", out, err)
	}

	cleanedConfigImage := stripTrailingCharacters(out)
	if cleanedConfigImage != imageReference {
		t.Fatalf("Unexpected Config.Image: %s (expected %s)", cleanedConfigImage, imageReference)
	}

	logDone("by_digest - create by digest")
}

func TestRunByDigest(t *testing.T) {

	logDone("by_digest - run by digest")
}

func TestRemoveImageByDigest(t *testing.T) {

	logDone("by_digest - remove image by digest")
}

func TestBuildByDigest(t *testing.T) {

	logDone("by_digest - build by digest")
}

func TestTagByDigest(t *testing.T) {

	logDone("by_digest - tag by digest")
}
