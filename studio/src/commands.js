export function commandLeaves(node, prefix, out) {
  const path = prefix.concat(node.name)
  if (node.commands?.length) {
    for (const child of node.commands) commandLeaves(child, path, out)
  } else {
    out.push({ path, node })
  }
  return out
}

export function effectiveInputs(leaf, ancestors) {
  // Ancestors' options/switches are inherited; the leaf adds its own
  // arguments/options. Shadowing is a core diagnostic, so names are unique.
  const inputs = []
  for (const node of ancestors.concat(leaf)) {
    for (const input of node.inputs || []) inputs.push(input)
  }
  return inputs
}

export function findByPath(tree, names) {
  let node = tree
  for (const name of names.slice(1)) {
    node = (node.commands || []).find((child) => child.name === name)
    if (!node) return null
  }
  return node
}
