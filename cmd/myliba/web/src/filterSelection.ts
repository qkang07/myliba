export function toggleFilter(values: string[], key: string, multiple: boolean, category = ""): string[] {
  if (values.includes(key)) return values.filter((value) => value !== key);
  const retained = multiple ? values : category ? values.filter((value) => !value.startsWith(category)) : [];
  return [...retained, key];
}

export function singleFilters(values: string[], categorized = false): string[] {
  return values.filter((value, index) => {
    const group = categorized ? value.split(":", 1)[0] : "tags";
    return !values.slice(index + 1).some((later) => (categorized ? later.split(":", 1)[0] : "tags") === group);
  });
}
