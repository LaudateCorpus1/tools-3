package kv

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"

	"github.com/cdnjs/tools/compress"

	"github.com/cdnjs/tools/packages"
	"github.com/cdnjs/tools/sentry"
	"github.com/cdnjs/tools/util"
)

// InsertVersionFromDisk is a helper tool to insert a single version from disk.
func InsertVersionFromDisk(logger *log.Logger, pckgName, pckgVersion string, metaOnly, srisOnly, filesOnly, count, noPush, panicOversized bool) {
	ctx := util.ContextWithEntries(util.GetStandardEntries(pckgName, logger)...)

	pckg, err := GetPackage(ctx, pckgName)
	util.Check(err)

	versions, err := pckg.Versions()
	if err != nil {
		// FIXME: handle err
		panic(err)
	}
	var found bool
	for _, version := range versions {
		if version == pckgVersion {
			found = true
			break
		}
	}

	if !found {
		panic(fmt.Sprintf("package `%s` version `%s` not found in git", pckgName, pckgVersion))
	}

	basePath := util.GetCDNJSLibrariesPath()
	dir := path.Join(basePath, *pckg.Name, pckgVersion)
	_, _, _, _, theoreticalSRIKeys, theoreticalFileKeys, err := InsertNewVersionToKV(ctx, *pckg.Name, pckgVersion, dir, metaOnly, srisOnly, filesOnly, noPush, panicOversized)
	if err != nil {
		panic(fmt.Sprintf("Failed to insert %s (%s): %s", *pckg.Name, pckgVersion, err))
	}

	util.Infof(ctx, fmt.Sprintf("Uploaded %s (%s).\n", pckgName, pckgVersion))
	if count {
		util.Infof(ctx, fmt.Sprintf("\ttheoretical SRI keys=%d\n\ttheoretical file keys=%d.\n", theoreticalSRIKeys, theoreticalFileKeys))
	}
}

type uploadResult struct {
	Name                string
	TheoreticalSRIKeys  int
	TheoreticalFileKeys int
}

type uploadWork struct {
	Index int
	Name  string
}

// InsertFromDisk is a helper tool to insert a number of packages from disk.
// Note: Only inserting versions (not updating package metadata).
func InsertFromDisk(logger *log.Logger, pckgs []string, metaOnly, srisOnly, filesOnly, count, noPush, panicOversized bool) {
	basePath := util.GetCDNJSLibrariesPath()

	done := make(chan uploadResult)
	jobs := make(chan uploadWork, len(pckgs))

	log.Println("Starting...")

	// spawn workers
	for w := 0; w < runtime.NumCPU()*10; w++ {
		go func() {
			for j := range jobs {
				func() {
					i, pckgName := j.Index, j.Name
					var pckgTotalSRIKeys, pckgTotalFileKeys int
					defer func() {
						done <- uploadResult{
							Name:                pckgName,
							TheoreticalSRIKeys:  pckgTotalSRIKeys,
							TheoreticalFileKeys: pckgTotalFileKeys,
						}
					}()

					ctx := util.ContextWithEntries(util.GetStandardEntries(pckgName, logger)...)
					pckg, readerr := GetPackage(ctx, pckgName)
					if readerr != nil {
						util.Infof(ctx, "p(%d/%d) failed to get package %s: %s\n", i+1, len(pckgs), pckgName, readerr)
						sentry.NotifyError(fmt.Errorf("failed to get package from KV: %s: %s", pckgName, readerr))
						return
					}

					versions, err := pckg.Versions()
					if err != nil {
						// FIXME: handle err
						panic(err)
					}
					for j, version := range versions {
						util.Debugf(ctx, "p(%d/%d) v(%d/%d) Inserting %s (%s)\n", i+1, len(pckgs), j+1, len(versions), *pckg.Name, version)
						dir := path.Join(basePath, *pckg.Name, version)
						_, _, _, _, theoreticalSRIKeys, theoreticalFileKeys, err := InsertNewVersionToKV(ctx, *pckg.Name, version, dir, metaOnly, srisOnly, filesOnly, noPush, panicOversized)
						pckgTotalSRIKeys += theoreticalSRIKeys
						pckgTotalFileKeys += theoreticalFileKeys

						if err != nil {
							util.Infof(ctx, "p(%d/%d) v(%d/%d) failed to insert %s (%s): %s\n", i+1, len(pckgs), j+1, len(versions), *pckg.Name, version, err)
							sentry.NotifyError(fmt.Errorf("p(%d/%d) v(%d/%d) failed to insert %s (%s) to KV: %s", i+1, len(pckgs), j+1, len(versions), *pckg.Name, version, err))
							return
						}
					}
				}()
			}
		}()
	}

	for index, name := range pckgs {
		jobs <- uploadWork{
			Index: index,
			Name:  name,
		}
	}
	close(jobs)

	var totalSRIKeys, totalFileKeys int

	// show some progress
	for i := 0; i < len(pckgs); i++ {
		res := <-done
		log.Printf("Completed (%d/%d): %s (sris_keys=%d, file_keys=%d)\n", i+1, len(pckgs), res.Name, res.TheoreticalSRIKeys, res.TheoreticalFileKeys)
		totalSRIKeys += res.TheoreticalSRIKeys
		totalFileKeys += res.TheoreticalFileKeys
	}
	close(done)

	log.Println("Done.")

	if count {
		log.Printf("Summary\n\tTotal Theoretical SRI Keys: %d\n\tTotal Theoretical File Keys: %d\n", totalSRIKeys, totalFileKeys)
	}
}

// InsertAggregateMetadataFromScratch is a helper tool to insert a number of packages' aggregated metadata
// into KV from scratch. The tool will scrape all metadata for each package from KV to create the aggregated entry.
func InsertAggregateMetadataFromScratch(logger *log.Logger, pckgs []string) {
	var wg sync.WaitGroup
	done := make(chan string)

	log.Println("Starting...")
	for index, name := range pckgs {
		wg.Add(1)
		go func(i int, pckgName string) {
			defer wg.Done()
			defer func() { done <- pckgName }()

			ctx := util.ContextWithEntries(util.GetStandardEntries(pckgName, logger)...)
			pckg, err := GetPackage(ctx, pckgName)
			if err != nil {
				util.Infof(ctx, "p(%d/%d) failed to get package %s: %s\n", i+1, len(pckgs), pckgName, err)
				sentry.NotifyError(fmt.Errorf("failed to get package from KV: %s: %s", pckgName, err))
				return
			}

			util.Debugf(ctx, "p(%d/%d) Fetching %s versions...\n", i+1, len(pckgs), *pckg.Name)
			versions, err := GetVersions(pckgName)
			util.Check(err)

			var assets []packages.Asset
			for j, version := range versions {
				util.Debugf(ctx, "p(%d/%d) v(%d/%d) Fetching %s (%s)\n", i+1, len(pckgs), j+1, len(versions), *pckg.Name, version)
				files, err := GetVersion(ctx, version)
				util.Check(err)
				assets = append(assets, packages.Asset{
					Version: strings.TrimPrefix(version, pckgName+"/"),
					Files:   files,
				})
			}

			pckg.Assets = assets
			successfulWrites, err := writeAggregatedMetadata(ctx, pckg)
			util.Check(err)

			if len(successfulWrites) == 0 {
				util.Infof(ctx, "p(%d/%d) %s: failed to write aggregated metadata", i+1, len(pckgs), *pckg.Name)
				sentry.NotifyError(fmt.Errorf("p(%d/%d) %s: failed to write aggregated metadata", i+1, len(pckgs), *pckg.Name))
			}
		}(index, name)
	}

	// show some progress
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < len(pckgs); i++ {
			name := <-done
			log.Printf("Completed (%d/%d): %s\n", i+1, len(pckgs), name)
		}
	}()

	wg.Wait()
	log.Println("Done.")
}

// OutputAllAggregatePackages outputs all the names of all aggregated package metadata entries in KV.
func OutputAllAggregatePackages() {
	res, err := listByPrefixNamesOnly("", aggregatedMetadataNamespaceID)
	util.Check(err)

	bytes, err := json.Marshal(res)
	util.Check(err)

	fmt.Printf("%s\n", bytes)
}

// OutputAllPackages outputs the names of all packages in KV.
func OutputAllPackages() {
	res, err := listByPrefixNamesOnly("", packagesNamespaceID)
	util.Check(err)

	bytes, err := json.Marshal(res)
	util.Check(err)

	fmt.Printf("%s\n", bytes)
}

// OutputFile outputs a file stored in KV.
func OutputFile(logger *log.Logger, fileKey string, ungzip, unbrotli bool) {
	ctx := util.ContextWithEntries(util.GetStandardEntries(fileKey, logger)...)

	util.Infof(ctx, "Fetching file from KV...\n")
	bytes, err := read(fileKey, filesNamespaceID)
	util.Check(err)

	if ungzip {
		util.Infof(ctx, "Decompressing gzip...\n")
		bytes = compress.UnGzip(bytes)
	} else if unbrotli {
		util.Infof(ctx, "Decompressing brotli...\n")
		file, err := ioutil.TempFile("", "")
		util.Check(err)
		defer os.Remove(file.Name())

		_, err = file.Write(bytes)
		util.Check(err)
		bytes = compress.UnBrotliCLI(ctx, file.Name())
	}

	fmt.Printf("%s\n", bytes)
}

// OutputAllFiles outputs all files stored in KV for a particular package.
func OutputAllFiles(logger *log.Logger, pckgName string) {
	ctx := util.ContextWithEntries(util.GetStandardEntries(pckgName, logger)...)

	// output all file names for each version in KV
	if versions, err := GetVersions(pckgName); err != nil {
		util.Infof(ctx, "Failed to get versions: %s\n", err)
	} else {
		for i, v := range versions {
			if files, err := GetFiles(v); err != nil {
				util.Infof(ctx, "(%d/%d) Failed to get version: %s\n", i+1, len(versions), err)
			} else {
				var output string
				if len(files) > 25 {
					output = fmt.Sprintf("(%d files)", len(files))
				} else {
					output = fmt.Sprintf("%v", files)
				}
				util.Infof(ctx, "(%d/%d) Found %s: %s\n", i+1, len(versions), v, output)
			}
		}
	}
}

// OutputAllMeta outputs all metadata associated with a package.
func OutputAllMeta(logger *log.Logger, pckgName string) {
	ctx := util.ContextWithEntries(util.GetStandardEntries(pckgName, logger)...)

	// output package metadata
	if pckg, err := GetPackage(ctx, pckgName); err != nil {
		util.Infof(ctx, "Failed to get package meta: %s\n", err)
	} else {
		util.Infof(ctx, "Parsed package: %s\n", pckg)
	}

	// output versions metadata
	if versions, err := GetVersions(pckgName); err != nil {
		util.Infof(ctx, "Failed to get versions: %s\n", err)
	} else {
		for i, v := range versions {
			if assets, err := GetVersion(ctx, v); err != nil {
				util.Infof(ctx, "(%d/%d) Failed to get version: %s\n", i+1, len(versions), err)
			} else {
				var output string
				if len(assets) > 25 {
					output = fmt.Sprintf("(%d assets)", len(assets))
				} else {
					output = fmt.Sprintf("%v", assets)
				}
				util.Infof(ctx, "(%d/%d) Parsed %s: %s\n", i+1, len(versions), v, output)
			}
		}
	}
}

// OutputAggregate outputs the aggregated metadata associated with a package.
func OutputAggregate(pckgName string) {
	bytes, err := read(pckgName, aggregatedMetadataNamespaceID)
	util.Check(err)

	uncompressed := compress.UnGzip(bytes)

	// check if it can unmarshal into a package successfully
	var p packages.Package
	util.Check(json.Unmarshal(uncompressed, &p))

	fmt.Printf("%s\n", uncompressed)
}

// OutputSRIs lists the SRIs namespace by prefix.
func OutputSRIs(prefix string) {
	res, err := listByPrefix(prefix, srisNamespaceID)
	util.Check(err)

	sris := make(map[string]string)
	for _, r := range res {
		sris[r.Name] = r.Metadata.(map[string]interface{})["sri"].(string)
	}

	bytes, err := json.Marshal(sris)
	util.Check(err)

	fmt.Printf("%s\n", bytes)
}
