/*
Copyright 2014 Zachary Klippenstein

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package regen

import (
	"fmt"
	"math"
	"math/rand"
	"regexp/syntax"
)

// generatorFactory is a function that creates a random string generator from a regular expression AST.
type generatorFactory func(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error)

// Must be initialized in init() to avoid "initialization loop" compile error.
var generatorFactories map[syntax.Op]generatorFactory

func init() {
	generatorFactories = map[syntax.Op]generatorFactory{
		syntax.OpEmptyMatch:     opEmptyMatch,
		syntax.OpLiteral:        opLiteral,
		syntax.OpAnyCharNotNL:   opAnyCharNotNl,
		syntax.OpAnyChar:        opAnyChar,
		syntax.OpQuest:          opQuest,
		syntax.OpStar:           opStar,
		syntax.OpPlus:           opPlus,
		syntax.OpRepeat:         opRepeat,
		syntax.OpCharClass:      opCharClass,
		syntax.OpConcat:         opConcat,
		syntax.OpAlternate:      opAlternate,
		syntax.OpCapture:        opCapture,
		syntax.OpBeginLine:      noop,
		syntax.OpEndLine:        noop,
		syntax.OpBeginText:      noop,
		syntax.OpEndText:        noop,
		syntax.OpWordBoundary:   noop,
		syntax.OpNoWordBoundary: noop,
	}
}

type runtimeArgs struct {
	Rng *rand.Rand
}

type internalGenerator struct {
	Name     string
	Generate func(args *runtimeArgs) string
}

func (gen *internalGenerator) String() string {
	return gen.Name
}

// Create a new generator for each expression in regexps.
func newGenerators(regexps []*syntax.Regexp, args *GeneratorArgs) ([]*internalGenerator, error) {
	generators := make([]*internalGenerator, len(regexps), len(regexps))
	var err error

	// create a generator for each alternate pattern
	for i, subR := range regexps {
		generators[i], err = newGenerator(subR, args)
		if err != nil {
			return nil, err
		}
	}

	return generators, nil
}

// Create a new generator for r.
func newGenerator(regexp *syntax.Regexp, args *GeneratorArgs) (generator *internalGenerator, err error) {
	simplified := regexp.Simplify()

	factory, ok := generatorFactories[simplified.Op]
	if ok {
		return factory(simplified, args)
	}

	return nil, fmt.Errorf("invalid generator pattern: /%s/ as /%s/\n%s",
		regexp, simplified, inspectRegexpToString(simplified))
}

// Generator that does nothing.
func noop(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	return &internalGenerator{regexp.String(), func(args *runtimeArgs) string {
		return ""
	}}, nil
}

func opEmptyMatch(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpEmptyMatch)
	return &internalGenerator{regexp.String(), func(args *runtimeArgs) string {
		return ""
	}}, nil
}

func opLiteral(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpLiteral)
	return &internalGenerator{regexp.String(), func(args *runtimeArgs) string {
		return runesToString(regexp.Rune...)
	}}, nil
}

func opAnyChar(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpAnyChar)
	return &internalGenerator{regexp.String(), func(args *runtimeArgs) string {
		return runesToString(rune(args.Rng.Int31()))
	}}, nil
}

func opAnyCharNotNl(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpAnyCharNotNL)
	charClass := newCharClass(1, rune(math.MaxInt32))
	return createCharClassGenerator(regexp.String(), charClass, args)
}

func opQuest(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpQuest)
	return createRepeatingGenerator(regexp, args, 0, 1)
}

func opStar(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpStar)
	return createRepeatingGenerator(regexp, args, 0, -1)
}

func opPlus(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpPlus)
	return createRepeatingGenerator(regexp, args, 1, -1)
}

func opRepeat(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpRepeat)
	return createRepeatingGenerator(regexp, args, regexp.Min, regexp.Max)
}

// Handles syntax.ClassNL because the parser uses that flag to generate character
// classes that respect it.
func opCharClass(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpCharClass)
	charClass := parseCharClass(regexp.Rune)
	return createCharClassGenerator(regexp.String(), charClass, args)
}

func opConcat(regexp *syntax.Regexp, genArgs *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpConcat)

	generators, err := newGenerators(regexp.Sub, genArgs)
	if err != nil {
		return nil, generatorError(err, "error creating generators for concat pattern /%s/", regexp)
	}

	return &internalGenerator{regexp.String(), func(runArgs *runtimeArgs) string {
		return genArgs.Executor.Execute(runArgs, generators)
	}}, nil
}

func opAlternate(regexp *syntax.Regexp, genArgs *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpAlternate)

	generators, err := newGenerators(regexp.Sub, genArgs)
	if err != nil {
		return nil, generatorError(err, "error creating generators for alternate pattern /%s/", regexp)
	}

	var numGens int = len(generators)

	return &internalGenerator{regexp.String(), func(runArgs *runtimeArgs) string {
		i := runArgs.Rng.Intn(numGens)
		generator := generators[i]
		return generator.Generate(runArgs)
	}}, nil
}

func opCapture(regexp *syntax.Regexp, args *GeneratorArgs) (*internalGenerator, error) {
	enforceOp(regexp, syntax.OpCapture)

	if err := enforceSingleSub(regexp); err != nil {
		return nil, err
	}

	return newGenerator(regexp.Sub[0], args)
}

// Panic if r.Op != op.
func enforceOp(r *syntax.Regexp, op syntax.Op) {
	if r.Op != op {
		panic(fmt.Sprintf("invalid Op: expected %s, was %s", opToString(op), opToString(r.Op)))
	}
}

// Return an error if r has 0 or more than 1 sub-expression.
func enforceSingleSub(regexp *syntax.Regexp) error {
	if len(regexp.Sub) != 1 {
		return generatorError(nil,
			"%s expected 1 sub-expression, but got %d: %s", opToString(regexp.Op), len(regexp.Sub), regexp)
	}
	return nil
}

func createCharClassGenerator(name string, charClass *tCharClass, args *GeneratorArgs) (*internalGenerator, error) {
	return &internalGenerator{name, func(args *runtimeArgs) string {
		i := args.Rng.Int31n(charClass.TotalSize)
		r := charClass.GetRuneAt(i)
		return runesToString(r)
	}}, nil
}

// Returns a generator that will run the generator for r's sub-expression [min, max] times.
func createRepeatingGenerator(regexp *syntax.Regexp, genArgs *GeneratorArgs, min int, max int) (*internalGenerator, error) {
	if err := enforceSingleSub(regexp); err != nil {
		return nil, err
	}

	generator, err := newGenerator(regexp.Sub[0], genArgs)
	if err != nil {
		return nil, generatorError(err, "Failed to create generator for subexpression: /%s/", regexp)
	}

	if max < 0 {
		max = maxUpperBound
	}

	return &internalGenerator{regexp.String(), func(runArgs *runtimeArgs) string {
		n := min + runArgs.Rng.Intn(max-min+1)
		return executeGeneratorRepeatedly(genArgs.Executor, runArgs, generator, n)
	}}, nil
}