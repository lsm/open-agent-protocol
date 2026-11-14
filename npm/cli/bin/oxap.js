#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");
const { binaryPath } = require("../index.js");

const result = spawnSync(binaryPath(), process.argv.slice(2), { stdio: "inherit" });
if (result.error) throw result.error;
if (result.signal) process.kill(process.pid, result.signal);
process.exit(result.status ?? 1);
