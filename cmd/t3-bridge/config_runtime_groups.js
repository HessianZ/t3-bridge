"use strict";

const filter = document.querySelector("#runtime-group-filter");
const groups = document.querySelectorAll("[data-runtime-group]");
filter?.addEventListener("input", () => {
  const query = filter.value.trim().toLowerCase();
  for (const group of groups) {
    group.hidden = !group.querySelector("h3").textContent.toLowerCase().includes(query);
  }
});

for (const group of groups) {
  const form = group.querySelector("form");
  const apply = form.querySelector("button");
  const update = () => { apply.disabled = !form.elements.access.value && !form.elements.mention.value; };
  form.addEventListener("change", update);
  update();
}
