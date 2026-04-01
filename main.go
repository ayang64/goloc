package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

type FileStats struct {
	Name       string
	Total      int
	Blank      int
	Code       int
}

type PackageStats struct {
	Path  string
	Files []FileStats
	Total int
	Blank int
	Code  int
}

type ModuleStats struct {
	Packages   []PackageStats
	TotalLines int
	BlankLines int
	CodeLines  int
}

var (
	ignoreInternal bool
)

func main() {
	includeTests := flag.Bool("t", false, "include test files (*_test.go)")
	flag.BoolVar(&ignoreInternal, "ignore-internal", false, "ignore internal directories")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		args = []string{"."}
	}

	var stats ModuleStats

	// Expand package patterns and process each package
	for _, pattern := range args {
		pkgs, err := expandPackages(pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error expanding package pattern %s: %v\n", pattern, err)
			continue
		}

		for _, pkg := range pkgs {
			// Skip if it's an internal package and we're ignoring them
			if ignoreInternal && isInternalPackage(pkg) {
				continue
			}

			pkgStats, err := processPackage(pkg, *includeTests)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error processing package %s: %v\n", pkg, err)
				continue
			}
			stats.Packages = append(stats.Packages, pkgStats)
			stats.TotalLines += pkgStats.Total
			stats.BlankLines += pkgStats.Blank
			stats.CodeLines += pkgStats.Code
		}
	}

	// Display results
	displayStats(stats)
}

func isInternalPackage(pkgPath string) bool {
	// Check if the path contains "internal" as a directory component
	parts := strings.Split(filepath.Clean(pkgPath), string(filepath.Separator))
	for _, part := range parts {
		if part == "internal" {
			return true
		}
	}
	return false
}

func expandPackages(pattern string) ([]string, error) {
	// Check if pattern ends with /...
	if strings.HasSuffix(pattern, "/...") {
		// Strip the /... and find all packages under that root
		root := strings.TrimSuffix(pattern, "/...")
		if root == "" {
			root = "."
		}
		return findPackages(root)
	}

	// Not a recursive pattern, return as-is
	return []string{pattern}, nil
}

func findPackages(root string) ([]string, error) {
	var packages []string
	seen := make(map[string]bool)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip vendor and hidden directories (but not the root)
		if d.IsDir() && path != root {
			name := d.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			// Skip internal directories if flag is set
			if ignoreInternal && name == "internal" {
				return filepath.SkipDir
			}
		}

		// Check if this directory contains .go files
		if d.IsDir() {
			hasGoFiles, err := hasGoFilesInDir(path)
			if err != nil {
				return err
			}
			if hasGoFiles && !seen[path] {
				packages = append(packages, path)
				seen[path] = true
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Strings(packages)
	return packages, nil
}

func hasGoFilesInDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}

func processPackage(pkgPath string, includeTests bool) (PackageStats, error) {
	stats := PackageStats{Path: pkgPath}

	// Get all .go files in the directory
	entries, err := os.ReadDir(pkgPath)
	if err != nil {
		return stats, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		// Skip test files unless includeTests is true
		if !includeTests && strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		filePath := filepath.Join(pkgPath, entry.Name())
		fileStats, err := processFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error processing %s: %v\n", filePath, err)
			continue
		}

		stats.Files = append(stats.Files, fileStats)
		stats.Total += fileStats.Total
		stats.Blank += fileStats.Blank
		stats.Code += fileStats.Code
	}

	return stats, nil
}

func processFile(filePath string) (FileStats, error) {
	stats := FileStats{Name: filepath.Base(filePath)}

	// Read the file to count total and blank lines
	file, err := os.Open(filePath)
	if err != nil {
		return stats, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 1
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		stats.Total++
		if line == "" {
			stats.Blank++
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return stats, err
	}

	// Parse the file with AST to count code lines
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return stats, err
	}

	// Count lines with actual code (excluding comments and blank lines)
	stats.Code = countCodeLines(fset, astFile, stats.Total)

	return stats, nil
}

func countCodeLines(fset *token.FileSet, file *ast.File, totalLines int) int {
	// Track which lines contain code or comments
	codeLines := make(map[int]bool)
	commentLines := make(map[int]bool)

	// Mark all comment lines
	for _, commentGroup := range file.Comments {
		for _, comment := range commentGroup.List {
			start := fset.Position(comment.Pos()).Line
			end := fset.Position(comment.End()).Line
			for line := start; line <= end; line++ {
				commentLines[line] = true
			}
		}
	}

	// Mark all lines with code
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		// Mark lines based on node type
		switch n.(type) {
		case *ast.GenDecl, *ast.FuncDecl, *ast.AssignStmt, *ast.ReturnStmt,
			*ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.DeferStmt,
			*ast.GoStmt, *ast.SendStmt, *ast.IncDecStmt, *ast.ExprStmt,
			*ast.LabeledStmt, *ast.BranchStmt, *ast.BlockStmt:
			start := fset.Position(n.Pos()).Line
			end := fset.Position(n.End()).Line
			for line := start; line <= end; line++ {
				codeLines[line] = true
			}
		}

		return true
	})

	// Return the count of lines with code
	return len(codeLines)
}

func displayStats(stats ModuleStats) {
	if len(stats.Packages) == 0 {
		fmt.Println("No packages found")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Display header
	fmt.Fprintln(w, "Package/File\tCode\tBlank\tTotal")
	fmt.Fprintln(w, strings.Repeat("-", 60))

	// Display each package
	for _, pkg := range stats.Packages {
		// Display package summary
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\n", pkg.Path, pkg.Code, pkg.Blank, pkg.Total)

		// Display individual files in the package
		for _, file := range pkg.Files {
			fmt.Fprintf(w, "  %s\t%d\t%d\t%d\n", file.Name, file.Code, file.Blank, file.Total)
		}

		// Add spacing between packages
		if len(stats.Packages) > 1 {
			fmt.Fprintln(w, "")
		}
	}

	// Display total if multiple packages
	if len(stats.Packages) > 1 {
		fmt.Fprintln(w, strings.Repeat("-", 60))
		fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\n", stats.CodeLines, stats.BlankLines, stats.TotalLines)
	}

	w.Flush()
}
