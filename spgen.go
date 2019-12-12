package main

import (
  "go/ast"
  "go/token"
  "go/parser"
  "fmt"
  "strings"
  "flag"
  "encoding/json"
  "path/filepath"
  "os"
  "path"
  "io/ioutil"
  "strconv"
)

// A'ight let's discuss a bit about this code.
// It is terrible, it was going good until i found that Go's parser.ParseDir doesn't parse recursive directories
// Had to mess around with walk() and then i thought of supporting subcategories
// Which introduced a shit ton of more headaches of path handling, i'm totally trash at paths.
// At first the idea was to use ./ relative imports if no -import specified but then running it from another directory causes issues
// So i made -import required, now the argument to the commands dir causes issues if ran from another directory
// So overall i decided to restrict it to be ran only in a directory that has a valid commands/ folder.
// I'm sorry but i couldn't do better, however i'm still open to feedback and contributions, this code needs a whole lotta cleanup.

type CommandInfo struct {
  Name string `json:"name"` // Command name
  Description string `json:"description"` // Command description
  OwnerOnly bool `json:"ownerOnly"` // If the command can only be executed by the owner.
  Category string `json:"category"` // The command's category, it is retrieved by package name title cased
  Aliases []string `json:"aliases"` // Aliases
  Usage string `json:"usage"` // Usage
  Package string `json:"package"` // The package selector string. e.g general.Ping
  Disabled bool `json:"disabled"` // If the command is disabled.
  Cooldown int `json:"cooldown"` // Command cooldown
  GuildOnly bool `json:"guildOnly"` // Wether the command can only be used in a guild
}

var encodeJson = flag.Bool("json", false, "Return the result in JSON.")
var baseImport = flag.String("import", "", "The base import path to use when importing command packages.")
var output = flag.String("o", "", "Where to output the file.")
var verbose = flag.Bool("v", false, "Verbose output")

func walk(dir string, out map[string]*ast.Package) {
  filepath.Walk(dir, func(fpath string, info os.FileInfo, err error) error {
    if err != nil { panic(err) }
    // Nested directories
    if info.IsDir() {
      if path.Clean(fpath) == path.Clean(dir) {
        return nil
      }
      // Parse the go files.
      pkgs, err := parser.ParseDir(token.NewFileSet(), fpath, nil, parser.ParseComments)
      if err == nil { for _, v := range pkgs { out[filepath.ToSlash(fpath)] = v } }
      // Check for further nested directories
      walk(fpath, out)
    }
    return nil
  })
}

func parseComments(cms *ast.CommentGroup) (description string, usage string, aliases []string, disabled bool, cooldown int, guildonly bool) {
  // At first i didn't need to parse too much attributes so i made them return values but it grew quickly, clean this.
  description = ""
  guildonly = false
  cooldown = 0
  aliases = []string{}
  disabled = false
  doc := cms.Text()
  if doc != "" && strings.HasSuffix(doc, "\n") {
    doc = doc[:len(doc) - 1]
  }

  split := strings.Split(doc, "\n")
  for _, line := range split {
    lower := strings.ToLower(line)
    // Usage
    if strings.HasPrefix(lower, "usage:") {
      usage = line[6:]
      if strings.HasPrefix(usage, " ") { usage = usage[1:] }
    } else if strings.HasPrefix(lower, "aliases:") {
      splitinp := line[8:]
      if strings.HasPrefix(splitinp, " ") { splitinp = splitinp[1:] }
      split := strings.Split(splitinp, ",")
      for _, alias := range split { aliases = append(aliases, strings.Trim(alias, " ")) }
    } else if lower == "disabled" {
      disabled = true
    } else if lower == "guild only" || lower == "guild only." {
      guildonly = true
    } else if strings.HasPrefix(lower, "cooldown:") {
      cut := lower[9:]
      cool, err := strconv.Atoi(strings.Trim(cut, " "))
      if err == nil { cooldown = cool }
    } else {
      description += line
    }
  }
  return
}

func main() {
  flag.Parse()
  if *baseImport == "" && !*encodeJson {
    fmt.Println("-import is required, pass it the base import of your project, e.g github.com/name/my_cool_bot")
    fmt.Println("This is required to know how to import the category subdirectories, when importing them the value of -import + /commands will be used, e.g import \"github.com/name/my_cool_bot/commands/general\"")
    fmt.Println("We decided that relative paths was too much of a headache so -import is required.")
    return
  }
  commands := []CommandInfo{}
  imports := []string{}
  //fset := token.NewFileSet()
  /*pkgs, err := parser.ParseDir(fset, "./commands", nil, parser.ParseComments)
  if err != nil {
    panic(err)
  }*/
  pkgs := map[string]*ast.Package{}
  if _, err := os.Lstat("./commands"); err != nil {
    fmt.Println("spgen must be ran inside a project with a commands/ folder")
    return
  }
  walk("./commands", pkgs)

  for pkgpath, pkg := range pkgs {
    if pkg.Name != "commands" {
      imports = append(imports, "\"" + (*baseImport + "/" + pkgpath) + "\"")
    }
    for _, file := range pkg.Files {
      sapphire := "sapphire" // The imported sapphire package name, local to a file.
      for _, dec := range file.Decls {
        // Try to detect the sapphire import, this is to parse package aliases correctly.
        if impdec, ok := dec.(*ast.GenDecl); ok {
          for _, spec := range impdec.Specs {
            if imp, ok := spec.(*ast.ImportSpec); ok { // Found an import spec.
              if id := imp.Name; imp.Name != nil { // Find the package name
                // This means there is a package alias name now, so try to make sure it's actually pointing to sapphire.
                if imp.Path.Kind == token.STRING {
                  if imp.Path.Value[1:][:len(imp.Path.Value) - 2] == "github.com/sapphire-cord/sapphire" {
                    // Got em, the sapphire import and it is aliased.
                    sapphire = id.Name // Treat sapphire with this alias now.
                  }
                }
              }
            }
          }
        }

        if fn, ok := dec.(*ast.FuncDecl); ok { // We found a function declaration.
          if !fn.Name.IsExported() {
            continue // Commands has to be exported to generate the registration.
          }
          if fn.Type.Results != nil {
            continue // Commands don't return any results.
          }
          if fn.Type.Params.NumFields() != 1 {
            continue // Commands only take one argument, a context.
          }
          // Verify that param is actually a sapphire context.
          if star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr); ok { // contexts are passed by pointer, therefore StarExpr
            // Got a pointer argument now verify it is a pointer to a sapphire context.
            if sel, ok := star.X.(*ast.SelectorExpr); ok {
              // The pointer is a selector, great, now make sure it is actually a selector to sapphire.CommandContext
              if pkg, ok := sel.X.(*ast.Ident); ok {
                if pkg.Name != sapphire {
                  continue // Not the sapphire package...
                }
                // The sapphire package! now verify the selector.
                if sel.Sel.Name != "CommandContext" {
                  continue // Not selecting the command context field.
                }
                // All good, we found a func CmdName(ctx *sapphire.CommandContext) declaration!
                owner := false
                /*doc := fn.Doc.Text()
                if doc != "" && strings.HasSuffix(doc, "\n") {
                  doc = doc[:len(doc) - 1] // Strip trailing newline.
                }
                doc = strings.Replace(doc, "\"", "\\\"", -1) // Escape quotes*/
                description, usage, aliases, disabled, cooldown, guildonly := parseComments(fn.Doc)
                name := strings.ToLower(fn.Name.Name)
                if strings.HasPrefix(name, "owner") {
                  owner = true
                  name = name[5:]
                }
                if *verbose { fmt.Printf("Found sapphire command: %s\n", name) }
                cat := strings.ToUpper(string(file.Name.Name[0])) + file.Name.Name[1:]
                p := file.Name.Name + "." + fn.Name.Name
                commands = append(commands, CommandInfo{Name:name, Description:description, OwnerOnly:owner, Category:cat, Package:p, Usage:usage, Aliases:aliases, Disabled:disabled, Cooldown:cooldown, GuildOnly:guildonly})
              }
            }
          }
        }
      }
    }
  }
  if *encodeJson {
    j, e := json.Marshal(commands)
    if e != nil {
      panic(e)
    }
    out := *output
    if out == "" {
      fmt.Println(string(j))
    } else {
      ioutil.WriteFile(out, j, 0666)
      fmt.Printf("Wrote JSON to %s\n", out)
    }
    return
  }

  var src = fmt.Sprintf(`// Package commands is the main entry point where all commands are registered.
// Auto-Generated with spgen DO NOT EDIT.
// To use this file import the package in your entry file and initialize it with commands.Init(bot)
package commands

import (
  "github.com/sapphire-cord/sapphire"
  %s
)

func Init(bot *sapphire.Bot) {
`, strings.Join(imports, "\n  "))
  for _, cmd := range commands {
    src += fmt.Sprintf("  bot.AddCommand(sapphire.NewCommand(\"%s\", \"%s\", %s)", cmd.Name, cmd.Category, cmd.Package)
    if cmd.Description != "" { src += fmt.Sprintf(".SetDescription(\"%s\")", strings.Replace(cmd.Description, "\"", "\\\"", -1)) }
    if cmd.OwnerOnly { src += ".SetOwnerOnly(true)" }
    if cmd.Usage != "" { src += fmt.Sprintf(".SetUsage(\"%s\")", strings.Replace(cmd.Usage, "\"", "\\\"", -1)) }
    if cmd.Disabled { src += ".Disable()" }
    if cmd.Cooldown > 0 { src += fmt.Sprintf(".SetCooldown(%d)", cmd.Cooldown) }
    if cmd.GuildOnly { src += ".SetGuildOnly(true)" }
    if len(cmd.Aliases) > 0 {
      aliases := cmd.Aliases
      for i, al := range aliases { aliases[i] = "\"" + strings.Replace(al, "\"", "\\\"", -1) + "\"" }
      src += fmt.Sprintf(".AddAliases(%s)", strings.Join(cmd.Aliases, ", ")) }
    src += ")\n"
  }
  src += "}"
  if *verbose { fmt.Println(src) }
  out := *output
  if out == "" {
    out = "./commands/init.go"
  }
  if err := ioutil.WriteFile(out, []byte(src), 0666); err != nil {
    panic(err)
  } else {
    fmt.Printf("Wrote output to %s\n", out)
    fmt.Println("")
    fmt.Println("spgen is still in early development, please report any issues you find at https://github.com/pollen5/spgen/issues")
  }
}
