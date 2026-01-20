# REMINDER

* Orig bug fixed, but updates revealed issue w/ dagop where the wrong server seems to be used.
* Orig repro: `while true; do docker rm -fv dagger-engine.dev; docker volume rm dagger-engine.dev; ./hack/dev; ./hack/with-dev go test -parallel=24 -run='TestModule/(TestConstructor|TestModuleDeprecationIntrospection)' -count=1 ./core/integration/ || break; done`
* New issue repro: `dagger call engine-dev test --parallel=12 --pkg=./core/integration --run='TestModule/TestTypedefSourceMaps/typescript_dep_with_typescript_generation'`
* Gonna go see if we can rm the solver instead, which should fix this issue entirely
