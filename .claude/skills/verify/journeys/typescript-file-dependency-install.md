## TypeScript file-dependency install journey

After changing `clients/ts` packaging or build preparation, run:

```sh
(cd clients/ts && npm ci --ignore-scripts && npm test)
```

The explicit test-file list also runs `test/prepare.test.mjs`. It copies the
client into temporary checkouts and runs real offline `npm install file:...`
commands from separate consumers, with isolated HOME/npm config/cache. A fresh
build must install and import without the compiler. Changed sources, modified
output or missing JavaScript/declarations must fail installation without it. With the compiler,
prepare must rebuild: the consumer must import the new source value and missing
outputs must be restored. Missing source/configuration inputs must refuse with
instructions to restore a complete checkout and prepare it. Temporary checkouts and children are cleaned up. Keep
the console output as install-surface evidence, alongside the full baseline.
