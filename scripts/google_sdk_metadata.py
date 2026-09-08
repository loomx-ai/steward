"""Read official Cloud SDK ApiMethodInfo/message declarations without executing code.

Some public REST methods are absent from anonymous Discovery responses. Their
pinned official SDK declarations provide method IDs, wire paths and JSON types.
The result uses Discovery's schema vocabulary, with explicit SDK provenance.
"""
import ast
import copy
import re


def convert(client_source, message_source, selected):
    client = ast.parse(client_source)
    messages = ast.parse(message_source)
    classes = {}

    def collect(nodes, parent=""):
        for node in nodes:
            if isinstance(node, ast.ClassDef):
                name = parent + node.name
                classes[name] = node
                collect(node.body, name + ".")
    collect(messages.body)
    schemas = {}

    def resolve(reference, owner):
        for prefix in [owner, *[owner.rsplit(".", i)[0] for i in range(1, owner.count(".") + 1)], ""]:
            candidate = (prefix + "." if prefix else "") + reference
            if candidate in classes:
                return candidate
        raise ValueError(f"SDK message {owner} has unresolved reference {reference}")

    def field(call, owner):
        if not isinstance(call.func, ast.Attribute):
            raise ValueError("Unsupported SDK field declaration")
        kind = call.func.attr
        options = {item.arg: ast.literal_eval(item.value) for item in call.keywords if item.arg != "variant"}
        if kind == "MessageField":
            reference = ast.literal_eval(call.args[0])
            if reference == "extra_types.JsonValue":
                result = {}
            else:
                target = resolve(reference, owner)
                schema(target)
                result = {"$ref": target}
        elif kind == "EnumField":
            target = classes[resolve(ast.literal_eval(call.args[0]), owner)]
            result = {"type": "string", "enum": [node.targets[0].id for node in target.body if isinstance(node, ast.Assign) and isinstance(node.targets[0], ast.Name)]}
        else:
            types = {"StringField": "string", "BooleanField": "boolean", "IntegerField": "integer", "FloatField": "number", "BytesField": "string"}
            if kind not in types:
                raise ValueError(f"Unsupported SDK field {kind}")
            result = {"type": types[kind]}
            if kind == "BytesField":
                result["format"] = "byte"
            if kind == "IntegerField":
                variant = next((item.value.attr for item in call.keywords if item.arg == "variant" and isinstance(item.value, ast.Attribute)), "INT64")
                if variant in ("INT64", "UINT64", "SINT64", "FIXED64", "SFIXED64"):
                    result.update(type="string", format="uint64" if variant in ("UINT64", "FIXED64") else "int64")
                else:
                    result["format"] = "int32"
        if options.get("repeated"):
            result = {"type": "array", "items": result}
        if options.get("required"):
            result["required"] = True
        return result

    def schema(name):
        if name in schemas:
            return schemas[name]
        node = classes[name]
        result = {"id": name, "type": "object", "properties": {}}
        schemas[name] = result
        if ast.get_docstring(node):
            result["description"] = ast.get_docstring(node)
        for declaration in node.body:
            if isinstance(declaration, ast.Assign) and isinstance(declaration.targets[0], ast.Name) and isinstance(declaration.value, ast.Call):
                result["properties"][declaration.targets[0].id] = field(declaration.value, name)
        for decorator in node.decorator_list:
            if isinstance(decorator, ast.Call) and isinstance(decorator.func, ast.Attribute) and decorator.func.attr == "MapUnrecognizedFields":
                field_name = ast.literal_eval(decorator.args[0])
                entry = result["properties"][field_name]["items"]["$ref"]
                result["additionalProperties"] = copy.deepcopy(schemas[entry]["properties"]["value"])
                del result["properties"][field_name]
        return result

    roots = [node for node in client.body if isinstance(node, ast.ClassDef)]
    if len(roots) != 1:
        raise ValueError("SDK source must declare one client")
    constants = {node.targets[0].id: ast.literal_eval(node.value) for node in roots[0].body if isinstance(node, ast.Assign) and isinstance(node.targets[0], ast.Name) and isinstance(node.value, ast.Constant)}
    methods = {}
    for node in ast.walk(client):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != "ApiMethodInfo":
            continue
        info = {item.arg: ast.literal_eval(item.value) for item in node.keywords}
        if info["method_id"] not in selected:
            continue
        request = schema(info["request_type_name"])
        parameters = {}
        for position, names in [("path", info["path_params"]), ("query", info["query_params"])]:
            for name in names:
                value = copy.deepcopy(request["properties"][name])
                value["location"] = position
                if position == "path":
                    value["required"] = True
                    marker = "{+" + name + "}"
                    if marker in info["relative_path"]:
                        if len(info["path_params"]) != 1:
                            raise ValueError("SDK raw path with multiple parameters requires explicit handling")
                        prefix, suffix = info["relative_path"].split(marker)
                        flat = info["flat_path"]
                        if not flat.startswith(prefix) or not flat.endswith(suffix):
                            raise ValueError("SDK flat and relative paths disagree")
                        expanded = flat[len(prefix):len(flat)-len(suffix) if suffix else None]
                        value["pattern"] = "^" + "".join("[^/]+" if token.startswith("{") else re.escape(token) for token in re.split(r"(\{[^}]+\})", expanded)) + "$"
                parameters[name] = value
        response = info["response_type_name"]
        schema(response)
        method = {"id": info["method_id"], "httpMethod": info["http_method"], "path": info["relative_path"], "parameters": parameters, "response": {"$ref": response}}
        if info["request_field"]:
            method["request"] = copy.deepcopy(request["properties"][info["request_field"]])
        methods[info["method_id"]] = method
    if set(methods) != set(selected):
        raise ValueError(f"Missing SDK methods: {sorted(set(selected)-set(methods))}")
    return {"name": constants["_PACKAGE"], "version": constants["_VERSION"], "rootUrl": constants["BASE_URL"], "servicePath": "", "methods": methods, "schemas": schemas}
