#!/usr/bin/env node
"use strict";

const { spawn } = require("node:child_process");
const { binaryPath } = require("../index.js");

const child = spawn(binaryPath(), process.argv.slice(2), { stdio: "inherit" });
const forwarded = ["SIGINT", "SIGTERM", "SIGHUP"];
const forward = (signal) => child.kill(signal);
for (const signal of forwarded) process.on(signal, forward);

child.on("error", (error) => {
  throw error;
});
child.on("exit", (code, signal) => {
  for (const name of forwarded) process.off(name, forward);
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 1);
});
