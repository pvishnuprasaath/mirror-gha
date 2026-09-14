// No npm dependencies on purpose — this fixture has to run with nothing
// but the Node runtime mirror-gha copies in. It uses the exact same
// GITHUB_OUTPUT file-append mechanism @actions/core's setOutput() uses
// internally, just written by hand.
const fs = require('fs');

const who = process.env['INPUT_WHO-TO-GREET'] || 'World';
const greeting = `Hello, ${who}!`;
console.log(greeting);

const outputFile = process.env['GITHUB_OUTPUT'];
if (outputFile) {
  fs.appendFileSync(outputFile, `greeting=${greeting}\n`);
}
