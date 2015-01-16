/*
   Copyright (c) 2015, Percona LLC and/or its affiliates. All rights reserved.

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU Affero General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Affero General Public License for more details.

   You should have received a copy of the GNU Affero General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>
*/

package pct_test

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/percona/percona-agent/pct"
	. "gopkg.in/check.v1"
)

type TestSuite struct {
	baseDir     string
	tmpDir      string
	testPidFile *pct.PidFile
	tmpFile     *os.File
}

var _ = Suite(&TestSuite{})

func (s *TestSuite) SetUpSuite(t *C) {
	// We can't/shouldn't use /usr/local/percona/ (the default basedir), so use
	// a tmpdir instead with roughly the same structure.
	basedir, err := ioutil.TempDir("", "pidfile-test-")
	t.Assert(err, IsNil)
	s.baseDir = basedir
	if err := pct.Basedir.Init(s.baseDir); err != nil {
		t.Errorf("Could initialize tmp Basedir: %v", err)
	}
	// We need and extra tmpdir for tests (!= basedir)
	tmpdir, err := ioutil.TempDir("", "pidfile-test-")
	t.Assert(err, IsNil)
	s.tmpDir = tmpdir
}

func (s *TestSuite) TearDownSuite(t *C) {
	if err := os.RemoveAll(s.baseDir); err != nil {
		t.Error(err)
	}

	if err := os.RemoveAll(s.tmpDir); err != nil {
		t.Error(err)
	}
}

func (s *TestSuite) SetUpTest(t *C) {
	s.testPidFile = pct.NewPidFile()
}

func (s *TestSuite) TestGet(t *C) {
	t.Assert(s.testPidFile.Get(), Equals, "")
}

func removeTmpFile(tmpFileName string, t *C) error {
	if err := os.Remove(tmpFileName); err != nil {
		t.Logf("Could not delete tmp file: %v", err)
		return err
	}
	return nil
}

func getTmpFileName() string {
	return fmt.Sprintf("%v.pid", rand.Int())
}

func getTmpAbsFileName(tmpDir string) string {
	return filepath.Join(tmpDir, getTmpFileName())
}

func (s *TestSuite) TestSetEmpty(t *C) {
	t.Assert(s.testPidFile.Set(""), Equals, nil)
}

func (s *TestSuite) TestSetExistsAbs(t *C) {
	tmpFile, err := ioutil.TempFile(s.tmpDir, "")
	if err != nil {
		t.Errorf("Could not create a tmp file: %v", err)
	}
	t.Assert(s.testPidFile.Set(tmpFile.Name()), NotNil, Commentf("Set should have failed, pidfile exists"))
}

func (s *TestSuite) TestSetNotExistsAbs(t *C) {
	randPidFileName := getTmpAbsFileName(s.tmpDir)
	t.Assert(s.testPidFile.Set(randPidFileName), Equals, nil, Commentf("Set should not have failed, pidfile does not exist"))
}

func (s *TestSuite) TestSetNotExistsRel(t *C) {
	randPidFileName := getTmpFileName()
	t.Assert(s.testPidFile.Set(randPidFileName), Equals, nil, Commentf("Set should have failed, pidfile exists"))
}

func (s *TestSuite) TestSetExistsRel(t *C) {
	tmpFile, err := ioutil.TempFile(pct.Basedir.Path(), "")
	if err != nil {
		t.Errorf("Could not create a tmp file: %v", err)
	}
	t.Assert(s.testPidFile.Set(tmpFile.Name()), NotNil, Commentf("Set should have failed, pidfile exists"))
}

func (s *TestSuite) TestRemoveEmpty(t *C) {
	t.Check(s.testPidFile.Set(""), Equals, nil)
	t.Assert(s.testPidFile.Remove(), Equals, nil, Commentf("Remove should have not failed, empty pidfile string provided"))
}

func (s *TestSuite) TestRemoveRel(t *C) {
	tmpFileName := getTmpFileName()
	t.Check(s.testPidFile.Set(tmpFileName), Equals, nil)
	t.Assert(s.testPidFile.Remove(), Equals, nil, Commentf("Remove should have not failed, pidfile exists"))
	absFilePath := filepath.Join(pct.Basedir.Path(), tmpFileName)
	t.Assert(pct.FileExists(absFilePath), Equals, false, Commentf("Remove should have deleted pidfile"))
}

func (s *TestSuite) TestRemoveAbs(t *C) {
	absFilePath := getTmpAbsFileName(s.tmpDir)
	t.Check(s.testPidFile.Set(absFilePath), Equals, nil)
	t.Assert(s.testPidFile.Remove(), Equals, nil, Commentf("Remove should have not failed, pidfile exists"))
	t.Assert(pct.FileExists(absFilePath), Equals, false, Commentf("Remove should have deleted pidfile"))
}

func (s *TestSuite) TestRemoveNotExistsRel(t *C) {
	randPidFileName := getTmpFileName()
	t.Check(s.testPidFile.Set(randPidFileName), Equals, nil)
	absFilePath := filepath.Join(pct.Basedir.Path(), randPidFileName)
	t.Check(removeTmpFile(absFilePath, t), Equals, nil)
	t.Assert(s.testPidFile.Remove(), Equals, nil, Commentf("Remove should have succedeed even when pidfile is missing"))
}

func (s *TestSuite) TestRemoveNotExistsAbs(t *C) {
	absFilePath := getTmpAbsFileName(s.tmpDir)
	t.Check(s.testPidFile.Set(absFilePath), Equals, nil)
	t.Check(removeTmpFile(absFilePath, t), Equals, nil)
	t.Assert(s.testPidFile.Remove(), Equals, nil, Commentf("Remove should have succedeed even when pidfile is missing"))
}
