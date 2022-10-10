import ts from "typescript";
import fs from "fs";

function generateDocumentation(
  fileNames: string[],
  options: ts.CompilerOptions
): void {
  // Build a program using the set of root file names in fileNames
  let program = ts.createProgram(fileNames, options);

  // Get the checker, we will use it to find more about classes
  let checker = program.getTypeChecker();
  let output: any[] = [];

  // Visit every sourceFile in the program
  for (const sourceFile of program.getSourceFiles()) {
    if (!sourceFile.isDeclarationFile) {
      // Walk the tree to search for classes
      ts.forEachChild(sourceFile, visit);
    }
  }

  // print out the doc
  console.log(JSON.stringify(output, undefined, 4));

  return;

  function visit(node: ts.Node) {
    // Only consider exported nodes
    if (!isNodeExported(node)) {
      return;
    }

    if (ts.isClassDeclaration(node) && node.name) {
      // TODO: better way of filtering out only the classes we want
      if (node.name.text === "Yarn") {
        ts.forEachChild(node, visitObjs);
      }
    }
  }

  function visitObjs(node: ts.Node) {
    /*
    console.log(
      ts.SyntaxKind[node.kind],
      "\n",
      checker.typeToString(checker.getTypeAtLocation(node))
    );
    */

    if (ts.isMethodDeclaration(node) && node.name) {
      if (!ts.isClassLike(node.parent)) {
        // TODO: support more than just class methods?
        return;
      }
      if (!ts.isIdentifier(node.name)) {
        // TODO: not sure how robust this is
        return;
      }
      console.log(node.parent.name?.text);
      console.log(node.name?.text);
      for (const param of node.parameters) {
        console.log("param: ", param.name.getText(), param.type?.getText());
      }
      console.log("return: ", node.type?.getText());
      console.log("");
    } else if (ts.isPropertyDeclaration(node) && node.name) {
      if (!ts.isClassLike(node.parent)) {
        // TODO: support more than just class parent?
        return;
      }
      if (!ts.isIdentifier(node.name)) {
        // TODO:
        return;
      }
      console.log(node.parent.name?.text);
      console.log(node.name?.text);
      console.log("type: ", node.type?.getText());
      console.log("");
    }

    ts.forEachChild(node, visitObjs);
  }

  /** Serialize a class symbol information */
  /** True if this is visible outside this file, false otherwise */
  function isNodeExported(node: ts.Node): boolean {
    return (
      (ts.getCombinedModifierFlags(node as ts.Declaration) &
        ts.ModifierFlags.Export) !==
        0 ||
      (!!node.parent && node.parent.kind === ts.SyntaxKind.SourceFile)
    );
  }
}

generateDocumentation(process.argv.slice(2), {
  target: ts.ScriptTarget.ES2015,
  module: ts.ModuleKind.CommonJS,
});
