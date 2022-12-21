package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"sort"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

/* Describing Tests as structures */
type Mock struct {
	name     string `json:"name"`
	model    bool
	source   bool
	filepath string            `json:"filepath"`
	types    map[string]string `json:"types"`
}

type Replacement struct {
	TableFullName  string
	ReplaceSql     string
	TableShortName string
}

type Output struct {
	name     string
	filetype string
}

type Test struct {
	name   string          `json:"name"`
	model  string          `json:"model"`
	mocks  map[string]Mock `json:"models"`
	output Output          `json:"output"`
}

/* Manifest parsing */
type Source struct {
	Name         string                 `json:"name"`
	UniqueId     string                 `json:"unique_id"`
	Fqn          []string               `json:"fqn"`
	RelationName string                 `json:"relation_name"`
	X            map[string]interface{} `json:"-"`
}

type Node struct {
	Compiledcode string                 `json:"compiled_code"`
	DependsOn    map[string][]string    `json:"depends_on"`
	Alias        string                 `json:"alias"`
	Database     string                 `json:"database"`
	Schema       string                 `json:"schema"`
	Name         string                 `json:"name"`
	X            map[string]interface{} `json:"-"`
}

type Manifest struct {
	Nodes   map[string]Node        `json:"nodes"`
	Sources map[string]Source      `json:"sources"`
	X       map[string]interface{} `json:"-"`
}

func parseManifest(path string) Manifest {
	jsonFile, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	bytes, _ := ioutil.ReadAll(jsonFile)
	manifest := Manifest{}
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		panic(err)
	}
	return manifest
}

func isModel(nodeKey string) bool {
	return strings.HasPrefix(nodeKey, "model")
}

func Replace(sql string, replacement Replacement) string {
	if (replacement == Replacement{}) {
		return sql
	}
	regexWithAlias, err := regexp.Compile(fmt.Sprintf(`(?i)%s\sas\s([A-z0-9_]+)\s`, replacement.TableFullName))
	if err != nil {
		panic(err)
	}
	useExistingAlias := fmt.Sprintf("(%s) AS $1 ", replacement.ReplaceSql)
	newSql := regexWithAlias.ReplaceAllString(sql, useExistingAlias)
	createAlias := fmt.Sprintf("(%s) AS %s", replacement.ReplaceSql, replacement.TableShortName)
	newSql = strings.ReplaceAll(newSql, fmt.Sprintf("%s", replacement.TableFullName), createAlias)
	return newSql
}

func sqlModel(manifest Manifest, nodeKey string, mocks map[string]Mock) (Replacement, error) {
	currentNode := manifest.Nodes[nodeKey]
	fullname := fmt.Sprintf("`%s`.`%s`.`%s`", currentNode.Database, currentNode.Schema, currentNode.Name)
	shortname := currentNode.Alias

	// if in mocks return the mock replacment
	if m, ok := mocks[nodeKey]; ok {
		r := Replacement{
			TableFullName:  fullname,
			ReplaceSql:     mockToSql(m),
			TableShortName: shortname}
		return r, nil
	}

	// if not a mock then recursively build query
	dependencies := currentNode.DependsOn["nodes"]
	node2sql := make(map[string]Replacement)
	for _, otherNodeKey := range dependencies {
		r, err := sql(manifest, otherNodeKey, mocks)
		if err != nil {
			return Replacement{}, err
		}
		node2sql[otherNodeKey] = r
	}

	sqlCode := currentNode.Compiledcode

	for _, replacement := range node2sql {
		sqlCode = Replace(sqlCode, replacement)

	}

	replacement := Replacement{ReplaceSql: sqlCode, TableFullName: fullname, TableShortName: shortname}
	return replacement, nil
}

func sqlSource(manifest Manifest, sourceKey string, mocks map[string]Mock) (Replacement, error) {
	source := manifest.Sources[sourceKey]
	if m, ok := mocks[sourceKey]; ok {
		r := Replacement{
			TableFullName:  source.RelationName,
			ReplaceSql:     mockToSql(m),
			TableShortName: source.Name}
		return r, nil
	}
	errors.New("something")
	return Replacement{}, nil //errors.New(fmt.Sprintf("%v not mocked", sourceKey))
}

func CSVToMap(reader io.Reader) []map[string]string {
	r := csv.NewReader(reader)
	rows := []map[string]string{}
	var header []string
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		if header == nil {
			header = record
		} else {
			dict := map[string]string{}
			for i := range header {
				dict[header[i]] = record[i]
			}
			rows = append(rows, dict)
		}
	}
	return rows
}

func mockToSql(m Mock) string {
	file, err := os.Open(m.filepath)
	if err != nil {
		panic(err)
	}
	data := CSVToMap(file)
	var sqlStatements []string
	for _, row := range data {
		columnsValues := []string{}
		columns := make([]string, 0)
		// ordering columns so we can test
		for k, _ := range  row {
			columns = append(columns, k)
		}
		sort.Strings(columns)
		for _, column := range columns {
			value := row[column]
			if columnType, ok := m.types[column]; ok {
				columnsValues = append(columnsValues, fmt.Sprintf("CAST(%s AS %s) AS %s", value, columnType, column))
				fmt.Println(columnsValues)
			} else {
				columnsValues = append(columnsValues, fmt.Sprintf("%s AS %s", value, column))
			}

		}
		statement := fmt.Sprintf("\n SELECT %s", strings.Join(columnsValues, ", "))
		sqlStatements = append(sqlStatements, statement)
	}
	return strings.Join(sqlStatements, "\n UNION ALL \n")
}

func sql(manifest Manifest, nodeKey string, mocks map[string]Mock) (Replacement, error) {
	if isModel(nodeKey) {
		return sqlModel(manifest, nodeKey, mocks)
	} else {
		return sqlSource(manifest, nodeKey, mocks)
	}
}

func parseTest(path string) (Test, error) {
	jsonFile, err := os.Open(path)
	if err != nil {
		return Test{}, err
	}
	bytes, _ := ioutil.ReadAll(jsonFile)
	test := Test{}
	if err := json.Unmarshal(bytes, &test); err != nil {
		return Test{}, err
	}
	return test, nil
}

func parseFolder(path string) ([]Test, error) {
	files, err := ioutil.ReadDir(path)
	tests := []Test{}
	if err != nil {
		return []Test{}, err
	}
	for _, f := range files {
		fullPath := filepath.Join(path, f.Name())
		test, err := parseTest(fullPath)
		if err != nil {
			return []Test{}, err
		}
		tests = append(tests, test)

	}
	return []Test{}, nil
}

func main() {
	test := Test{
		name:  "dummy_test",
		model: "dummy_model",
		mocks: map[string]Mock{
			"sme": Mock{name: "mock1", model: true},
		},
		output: Output{name: "first check", filetype: "csv"},
	}
	m := parseManifest("manifest.json")

	replacement, err := sql(m, "model.data_feeds.liq_evals_by_asset", test.mocks)

	if err != nil {
		panic(err)
	}
	fmt.Println("")
	fmt.Println("LAST RESULT")
	fmt.Println(fmt.Sprintf("%v", replacement.ReplaceSql))
}