#!/bin/bash

# Commit the test file and the fix for case sensitivity
git add lsps/manager/connection_manager.go lsps/manager/connection_manager_test.go
git commit -m "Refine ConnectionManager: Add tests and fix case-sensitivity"
