import { defineConfig } from "vitest/config";

// One config for the whole workspace rather than one per package. `lydite test`
// runs a component's suite once, at the component root, because a component is
// what its build tool treats as a whole — and npm treats this workspace as one.
//
// The coverage provider is named here and declared in package.json.
// `vitest --coverage` needs it installed, and lydite deliberately will not add
// it: installing a dependency into the repository it is about to gate would
// have lydite change what that repository resolves to.
export default defineConfig({
  test: {
    include: ["{libs/*,pr-relay,oauth-exchange}/src/**/*.test.ts"],
    coverage: {
      provider: "v8",
      include: ["{libs/*,pr-relay,oauth-exchange}/src/**/*.ts"],
      // testing.ts is test support — a real key pair for the suite to sign
      // with — so it is not code under measurement.
      exclude: ["**/*.test.ts", "**/testing.ts"],
    },
  },
});
