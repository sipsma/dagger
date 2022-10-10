import ts from "typescript";
function generateDocumentation(fileNames, options) {
    // Build a program using the set of root file names in fileNames
    let program = ts.createProgram(fileNames, options);
    // Get the checker, we will use it to find more about classes
    let checker = program.getTypeChecker();
    let output = [];
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
    function visit(node) {
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
    function visitObjs(node) {
        /*
        console.log(
          ts.SyntaxKind[node.kind],
          "\n",
          checker.typeToString(checker.getTypeAtLocation(node))
        );
        */
        var _a, _b, _c, _d, _e, _f, _g;
        if (ts.isMethodDeclaration(node) && node.name) {
            if (!ts.isClassLike(node.parent)) {
                // TODO: support more than just class methods?
                return;
            }
            if (!ts.isIdentifier(node.name)) {
                // TODO: not sure how robust this is
                return;
            }
            console.log((_a = node.parent.name) === null || _a === void 0 ? void 0 : _a.text);
            console.log((_b = node.name) === null || _b === void 0 ? void 0 : _b.text);
            for (const param of node.parameters) {
                console.log("param: ", param.name.getText(), (_c = param.type) === null || _c === void 0 ? void 0 : _c.getText());
            }
            console.log("return: ", (_d = node.type) === null || _d === void 0 ? void 0 : _d.getText());
            console.log("");
        }
        else if (ts.isPropertyDeclaration(node) && node.name) {
            if (!ts.isClassLike(node.parent)) {
                // TODO: support more than just class parent?
                return;
            }
            if (!ts.isIdentifier(node.name)) {
                // TODO:
                return;
            }
            console.log((_e = node.parent.name) === null || _e === void 0 ? void 0 : _e.text);
            console.log((_f = node.name) === null || _f === void 0 ? void 0 : _f.text);
            console.log("type: ", (_g = node.type) === null || _g === void 0 ? void 0 : _g.getText());
            console.log("");
        }
        ts.forEachChild(node, visitObjs);
    }
    /** Serialize a class symbol information */
    /** True if this is visible outside this file, false otherwise */
    function isNodeExported(node) {
        return ((ts.getCombinedModifierFlags(node) &
            ts.ModifierFlags.Export) !==
            0 ||
            (!!node.parent && node.parent.kind === ts.SyntaxKind.SourceFile));
    }
}
generateDocumentation(process.argv.slice(2), {
    target: ts.ScriptTarget.ES2015,
    module: ts.ModuleKind.CommonJS,
});
