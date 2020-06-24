package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cdnjs/tools/git"
	"github.com/cdnjs/tools/npm"
	"github.com/cdnjs/tools/packages"
	"github.com/cdnjs/tools/util"
)

var (
	// Store the number of validation errors
	errCount uint = 0
)

func main() {
	flag.Parse()
	subcommand := flag.Arg(0)

	if util.IsDebug() {
		fmt.Println("Running in debug mode")
	}

	// change output for readability in CI
	util.SetLoggerFlag(0)

	if subcommand == "lint" {
		lintPackage(flag.Arg(1))

		if errCount > 0 {
			os.Exit(1)
		}
		return
	}

	if subcommand == "show-files" {
		showFiles(flag.Arg(1))

		if errCount > 0 {
			os.Exit(1)
		}
		return
	}

	panic("unknown subcommand")
}

func showFiles(pckgPath string) {
	ctx := util.ContextWithName(pckgPath)
	pckg, readerr := packages.ReadPackageJSON(ctx, pckgPath)
	if readerr != nil {
		err(ctx, readerr.Error())
		return
	}

	if pckg.Autoupdate == nil {
		err(ctx, "autoupdate not found")
		return
	}

	if pckg.Autoupdate.Source == "npm" {
		npmVersions := npm.GetVersions(pckg.Autoupdate.Target)
		if len(npmVersions) == 0 {
			err(ctx, "no version found on npm")
			return
		}

		sort.Sort(npm.ByNpmVersion(npmVersions))

		if len(npmVersions) > util.IMPORT_ALL_MAX_VERSIONS {
			npmVersions = npmVersions[:util.IMPORT_ALL_MAX_VERSIONS]
		}

		// print info for the first version
		firstNpmVersion := npmVersions[0]
		{
			tarballDir := npm.DownloadTar(ctx, firstNpmVersion.Tarball)
			filesToCopy := pckg.NpmFilesFrom(tarballDir)

			if len(filesToCopy) == 0 {
				errormsg := ""
				errormsg += fmt.Sprintf("No files will be published for version %s.\n", firstNpmVersion.Version)

				for _, filemap := range pckg.NpmFileMap {
					for _, pattern := range filemap.Files {
						errormsg += fmt.Sprintf("[Click here to debug your glob pattern `%s`](%s).\n", pattern, makeGlobDebugLink(pattern, tarballDir))
					}
				}
				err(ctx, errormsg)
				goto moreNpmVersions
			}

			fmt.Printf("```\n")
			for _, file := range filesToCopy {
				fmt.Printf("%s\n", file.To)
			}
			fmt.Printf("```\n")
		}

	moreNpmVersions:
		// aggregate info for the few last version
		fmt.Printf("\n%d last versions:\n", util.IMPORT_ALL_MAX_VERSIONS)
		{
			for _, version := range npmVersions {
				tarballDir := npm.DownloadTar(ctx, version.Tarball)
				filesToCopy := pckg.NpmFilesFrom(tarballDir)

				fmt.Printf("- %s: %d file(s) matched", version.Version, len(filesToCopy))
				if len(filesToCopy) > 0 {
					fmt.Printf(" :heavy_check_mark:\n")
				} else {
					fmt.Printf(" :heavy_exclamation_mark:\n")
				}
			}
		}
	}

	if pckg.Autoupdate.Source == "git" {
		packageGitDir, direrr := ioutil.TempDir("", "git")
		util.Check(direrr)

		out, cloneerr := packages.GitClone(ctx, pckg, packageGitDir)
		if cloneerr != nil {
			err(ctx, fmt.Sprintf("could not clone repo: %s: %s\n", cloneerr, out))
			return
		}

		gitVersions := git.GetVersions(ctx, pckg, packageGitDir)

		if len(gitVersions) == 0 {
			err(ctx, "no version found on npm")
			return
		}

		sort.Sort(git.ByGitVersion(gitVersions))

		if len(gitVersions) > util.IMPORT_ALL_MAX_VERSIONS {
			gitVersions = gitVersions[:util.IMPORT_ALL_MAX_VERSIONS]
		}

		// print info for the first version
		firstNpmVersion := gitVersions[0]
		{
			filesToCopy := pckg.NpmFilesFrom(packageGitDir)

			if len(filesToCopy) == 0 {
				errormsg := ""
				errormsg += fmt.Sprintf("No files will be published for version %s.\n", firstNpmVersion.Version)

				for _, filemap := range pckg.NpmFileMap {
					for _, pattern := range filemap.Files {
						errormsg += fmt.Sprintf("[Click here to debug your glob pattern `%s`](%s).\n", pattern, makeGlobDebugLink(pattern, packageGitDir))
					}
				}
				err(ctx, errormsg)
				goto moreGitVersions
			}

			fmt.Printf("```\n")
			for _, file := range filesToCopy {
				fmt.Printf("%s\n", file.To)
			}
			fmt.Printf("```\n")
		}

	moreGitVersions:
		// aggregate info for the few last version
		fmt.Printf("\n%d last versions:\n", util.IMPORT_ALL_MAX_VERSIONS)
		{
			for _, version := range gitVersions {
				packages.GitForceCheckout(ctx, pckg, packageGitDir, version.Tag)
				filesToCopy := pckg.NpmFilesFrom(packageGitDir)

				fmt.Printf("- %s: %d file(s) matched", version.Version, len(filesToCopy))
				if len(filesToCopy) > 0 {
					fmt.Printf(" :heavy_check_mark:\n")
				} else {
					fmt.Printf(" :heavy_exclamation_mark:\n")
				}
			}
		}
	}
}

func makeGlobDebugLink(glob string, dir string) string {
	encodedGlob := url.QueryEscape(glob)
	allTests := ""

	util.Check(filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			allTests += "&tests=" + url.QueryEscape(info.Name())
		}
		return nil
	}))

	return fmt.Sprintf("https://www.digitalocean.com/community/tools/glob?comments=true&glob=%s&matches=true%s&tests=", encodedGlob, allTests)
}

func lintPackage(pckgPath string) {
	ctx := util.ContextWithName(pckgPath)

	util.Debugf(ctx, "Linting %s...\n", pckgPath)

	pckg, readerr := packages.ReadPackageJSON(ctx, pckgPath)
	if readerr != nil {
		err(ctx, readerr.Error())
		return
	}

	if pckg.Name == "" {
		err(ctx, shouldNotBeEmpty(".name"))
	}

	if pckg.Version != "" {
		err(ctx, shouldBeEmpty(".version"))
	}

	// if pckg.NpmName != nil && *pckg.NpmName == "" {
	// 	err(ctx, shouldBeEmpty(".NpmName"))
	// }

	// if len(pckg.NpmFileMap) > 0 {
	// 	err(ctx, shouldBeEmpty(".NpmFileMap"))
	// }

	if pckg.Autoupdate != nil {
		if pckg.Autoupdate.Source != "npm" && pckg.Autoupdate.Source != "git" {
			err(ctx, "Unsupported .autoupdate.source: "+pckg.Autoupdate.Source)
		}
	} else {
		warn(ctx, ".autoupdate should not be null. Package will never auto-update")
	}

	if pckg.Repository.Repotype != "git" {
		err(ctx, "Unsupported .repository.type: "+pckg.Repository.Repotype)
	}

	if pckg.Autoupdate != nil && pckg.Autoupdate.Source == "npm" {
		if !npm.Exists(pckg.Autoupdate.Target) {
			err(ctx, "package doesn't exists on npm")
		} else {
			counts := npm.GetMonthlyDownload(pckg.Autoupdate.Target)
			if counts.Downloads < 800 {
				err(ctx, "package download per month on npm is under 800")
			}
		}
	}

	const (
		pkgJSON = "package.json"
	)

	if ok := strings.HasSuffix(pckgPath, pkgJSON); !ok {
		panic("unexpected package json path")
	}

	// check file sizes
	versionDir := path.Join(pckgPath[:len(pckgPath)-len(pkgJSON)], pckg.Version)
	for _, fileMap := range pckg.NpmFileMap {
		for _, pattern := range fileMap.Files {
			basePath := path.Join(versionDir, fileMap.BasePath)

			// find files that match glob
			pkgCtx := util.ContextWithName(basePath)
			list, listerr := util.ListFilesGlob(pkgCtx, basePath, pattern)
			if listerr != nil {
				err(ctx, "glob: "+listerr.Error())
				return
			}

			// warn for files with sizes exceeding max file size
			for _, f := range list {
				fp := path.Join(basePath, f)

				info, staterr := os.Stat(fp)
				if staterr != nil {
					err(ctx, "stat: "+staterr.Error())
					return
				}

				size := info.Size()
				if size > util.MAX_FILE_SIZE {
					warn(ctx, fmt.Sprintf("file %s ignored due to byte size (%d > %d)", f, size, util.MAX_FILE_SIZE))
				} else {
					util.Debugf(ctx, fp, "ok")
				}
			}
		}
	}

}

func err(ctx context.Context, s string) {
	if prefix, ok := ctx.Value("loggerPrefix").(string); ok {
		fmt.Printf("::error file=%s,line=1,col=1::%s\n", prefix, escapeGitHub(s))
	} else {
		panic("unreachable")
	}
	errCount++
}

func warn(ctx context.Context, s string) {
	if prefix, ok := ctx.Value("loggerPrefix").(string); ok {
		fmt.Printf("::warning file=%s,line=1,col=1::%s\n", prefix, escapeGitHub(s))
	} else {
		panic("unreachable")
	}
}

func shouldBeEmpty(name string) string {
	return fmt.Sprintf("%s should be empty", name)
}

func shouldNotBeEmpty(name string) string {
	return fmt.Sprintf("%s should be specified", name)
}

func escapeGitHub(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\n", "%0A")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return s
}
