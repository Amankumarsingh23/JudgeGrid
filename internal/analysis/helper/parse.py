"""Trusted CST extractor. Never imports or executes analyzed source."""
import base64
import hashlib
import importlib.metadata as metadata
import json
import pathlib
import platform
import sys


def main():
    if sys.version_info[:2] != (3, 13):
        raise ValueError("unsupported interpreter")
    lock = json.loads(pathlib.Path(__file__).with_name("lock.json").read_text())
    target = "win32-AMD64" if sys.platform == "win32" else "linux-x86_64"
    if platform.machine().lower() not in ("amd64", "x86_64"):
        raise ValueError("unsupported architecture")
    for name, version in lock["packages"].items():
        dist = metadata.distribution(name)
        if dist.version != version:
            raise ValueError("dependency version")
        for path, digest in lock["platforms"][target][name]["files"].items():
            if hashlib.sha256(dist.locate_file(path).read_bytes()).hexdigest() != digest:
                raise ValueError("dependency digest")
    from tree_sitter import Language, Parser
    import tree_sitter_go
    import tree_sitter_python
    import tree_sitter_javascript
    import tree_sitter_typescript

    request = json.loads(sys.stdin.buffer.read(2 * 1024 * 1024 + 1))
    source = base64.b64decode(request["source"], validate=True)
    policy = request["policy"]
    language = request["language"]
    grammars = {"go": tree_sitter_go.language, "python": tree_sitter_python.language,
                "javascript": tree_sitter_javascript.language,
                "typescript": tree_sitter_typescript.language_typescript,
                "tsx": tree_sitter_typescript.language_tsx}
    if len(source) > policy["max_file_bytes"]:
        raise ValueError("budget")
    tree = Parser(Language(grammars[language]())).parse(source)
    root = tree.root_node
    result = dict(schema=1, status="PARTIAL" if root.has_error else "PARSED", package="",
                  nodes=0, symbols=[], imports=[], calls=[], bindings=[], references=[], errors=[])

    def text(node):
        return source[node.start_byte:node.end_byte].decode("utf-8") if node else ""

    def field(node, key):
        return node.child_by_field_name(key)

    def add_symbol(node, name, kind, parent, receiver="", receiver_var=""):
        if not name or not text(name) or len(text(name)) > 512:
            return parent
        i = len(result["symbols"])
        exported = "unknown"
        if language == "go":
            exported = "public" if text(name)[0].isupper() else "private"
        if language == "python":
            exported = "private" if text(name).startswith("_") else "public-by-convention"
        if node.parent and node.parent.type == "export_statement":
            exported = "exported"
        result["symbols"].append(dict(kind=kind, name=text(name), name_start=name.start_byte,
            name_end=name.end_byte, start=node.start_byte, end=node.end_byte, parent=parent,
            scope_start=scope.start_byte, scope_end=scope.end_byte,
            receiver=receiver, receiver_var=receiver_var, export=exported, partial=node.has_error))
        return i

    def binding(node, scope, typ="", receiver=False):
        if node and node.type in ("identifier", "field_identifier", "shorthand_property_identifier_pattern"):
            result["bindings"].append(dict(name=text(node), type=typ, start=node.start_byte,
                end=node.end_byte, scope_start=scope.start_byte, scope_end=scope.end_byte, receiver=receiver))

    # Stack traversal bounds both syntax (including unnamed nodes) and extraction.
    stack = [(root, -1, 0, root)]
    while stack:
        node, parent, depth, scope = stack.pop()
        result["nodes"] += 1
        if result["nodes"] > policy["max_nodes"] or depth > policy["max_depth"]:
            raise ValueError("budget")
        if node.is_error or node.is_missing:
            if len(result["errors"]) < 64:
                result["errors"].append(dict(kind="missing" if node.is_missing else "syntax", start=node.start_byte, end=node.end_byte))
        kind = node.type
        next_parent = parent
        next_scope = scope
        if kind in ("block", "statement_block", "function_definition", "function_declaration", "method_declaration", "method_definition", "arrow_function", "function_expression", "class_definition", "class_declaration"):
            next_scope = node
        if language == "go":
            if kind == "package_clause":
                result["package"] = text(node.named_children[-1])
            elif kind in ("function_declaration", "method_declaration"):
                receiver = field(node, "receiver")
                recv, var = "", ""
                if receiver and receiver.named_children:
                    param = receiver.named_children[0]
                    recv = text(field(param, "type")).lstrip("*")
                    var = text(field(param, "name"))
                next_parent = add_symbol(node, field(node, "name"), "method" if receiver else "function", parent, recv, var)
            elif kind in ("type_spec", "type_alias"):
                typ = field(node, "type")
                sk = {"struct_type": "struct", "interface_type": "interface"}.get(typ.type if typ else "", "type")
                next_parent = add_symbol(node, field(node, "name"), sk, parent)
            elif kind in ("const_spec", "var_spec"):
                for i, child in enumerate(node.children):
                    if node.field_name_for_child(i) == "name":
                        add_symbol(node, child, "constant" if kind == "const_spec" else "variable", parent)
                        binding(child, scope, text(field(node, "type")))
            elif kind == "import_spec":
                path = field(node, "path")
                module = text(path)[1:-1]
                alias = text(field(node, "name")) or module.rsplit("/", 1)[-1]
                result["imports"].append(dict(module=module, name="", alias=alias, start=node.start_byte, end=node.end_byte))
            elif kind == "parameter_declaration":
                for i, child in enumerate(node.children):
                    if node.field_name_for_child(i) == "name":
                        binding(child, scope, text(field(node, "type")).lstrip("*"), bool(node.parent and node.parent.parent and node.parent.parent.type == "method_declaration" and field(node.parent.parent, "receiver") == node.parent))
            elif kind == "short_var_declaration":
                left = field(node, "left")
                for child in left.named_children if left else []:
                    binding(child, scope)
            elif kind == "type_identifier" and not (node.parent and node.parent.type == "qualified_type"):
                result["references"].append(dict(name=text(node), start=node.start_byte, end=node.end_byte, parent=parent))
        elif language == "python":
            if kind == "class_definition":
                next_parent = add_symbol(node, field(node, "name"), "class", parent)
            elif kind == "function_definition":
                method = parent >= 0 and result["symbols"][parent]["kind"] == "class"
                next_parent = add_symbol(node, field(node, "name"), "method" if method else "function", parent)
            elif kind == "parameters":
                for child in node.named_children:
                    binding(child if child.type == "identifier" else field(child, "name"), scope)
            elif kind in ("assignment", "named_expression"):
                binding(field(node, "left") or field(node, "name"), scope)
            elif kind in ("import_statement", "import_from_statement"):
                module = text(field(node, "module_name")) if kind == "import_from_statement" else ""
                for i, child in enumerate(node.children):
                    if node.field_name_for_child(i) != "name":
                        continue
                    target = field(child, "name") if child.type == "aliased_import" else child
                    name = text(target)
                    alias = text(field(child, "alias")) if child.type == "aliased_import" else name.split(".")[0]
                    result["imports"].append(dict(module=module or name, name=name if module else "", alias=alias, start=child.start_byte, end=child.end_byte))
        else:
            if kind in ("function_declaration", "generator_function_declaration", "method_definition", "class_declaration", "interface_declaration", "type_alias_declaration", "enum_declaration"):
                sk = {"method_definition": "method", "class_declaration": "class", "interface_declaration": "interface", "type_alias_declaration": "type", "enum_declaration": "type"}.get(kind, "function")
                next_parent = add_symbol(node, field(node, "name"), sk, parent)
            elif kind == "variable_declarator":
                value = field(node, "value")
                next_parent = add_symbol(node, field(node, "name"), "function" if value and value.type in ("arrow_function", "function_expression") else "variable", parent)
                binding(field(node, "name"), scope)
            elif kind in ("required_parameter", "optional_parameter"):
                binding(field(node, "pattern") or field(node, "name"), scope)
            elif kind == "formal_parameters":
                for child in node.named_children:
                    binding(child, scope)
            elif kind == "import_statement":
                module = text(field(node, "source"))[1:-1]
                clause = next((x for x in node.named_children if x.type == "import_clause"), None)
                todo = list(clause.named_children) if clause else []
                while todo:
                    child = todo.pop(0)
                    if child.type == "identifier":
                        result["imports"].append(dict(module=module, name="default", alias=text(child), start=child.start_byte, end=child.end_byte))
                    elif child.type == "import_specifier":
                        name = field(child, "name")
                        result["imports"].append(dict(module=module, name=text(name), alias=text(field(child, "alias") or name), start=child.start_byte, end=child.end_byte))
                    elif child.type == "namespace_import":
                        result["imports"].append(dict(module=module, name="*", alias=text(child.named_children[-1]), start=child.start_byte, end=child.end_byte))
                    else:
                        todo.extend(child.named_children)
                if not clause:
                    result["imports"].append(dict(module=module, name="", alias="", start=node.start_byte, end=node.end_byte))
        if kind in ("call_expression", "call", "new_expression"):
            function = field(node, "function") or field(node, "constructor")
            if function and len(text(function)) <= 512:
                result["calls"].append(dict(target=text(function), start=function.start_byte, end=function.end_byte, parent=parent, partial=node.has_error))
        for key, limit in (("symbols", "max_symbols_per_file"), ("calls", "max_calls_per_file"), ("imports", "max_imports_per_file"), ("references", "max_references_per_file")):
            if len(result[key]) > policy[limit]:
                raise ValueError("budget")
        if len(result["bindings"]) > policy["max_nodes"]:
            raise ValueError("budget")
        for child in reversed(node.children):
            stack.append((child, next_parent, depth + 1, next_scope))
    sys.stdout.write(json.dumps(result, separators=(",", ":"), ensure_ascii=True))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        # Controlled failure codes; never echo source, names, paths or exceptions.
        sys.stdout.write('{"failure":"budget"}' if str(exc) == "budget" else '{"failure":"parser"}')
        sys.exit(0)
