function workspaceText(source) {
  return Object.hasOwn(workspaceMessages, source) ? workspaceMessages[source] : source;
}

const workspaceLanguage = document.querySelector("[data-workspace-language]");
if (workspaceLanguage) {
  const select = workspaceLanguage.querySelector("select");
  let savedLanguage;
  let edited = false;
  document.addEventListener("input", (event) => {
    if (event.target.closest("form") && !workspaceLanguage.contains(event.target)) edited = true;
  });
  async function readLanguage() {
    try {
      const response = await fetch("/workspace-language");
      if (!response.ok) throw new Error();
      savedLanguage = await response.text();
      select.value = savedLanguage;
      select.disabled = false;
    } catch {
      select.title = select.dataset.error;
    }
  }
  select.addEventListener("change", async () => {
    if (edited && !window.confirm(select.dataset.reload)) {
      select.value = savedLanguage;
      return;
    }
    select.disabled = true;
    try {
      const response = await fetch("/workspace-language", {
        method: "POST",
        referrerPolicy: "same-origin",
        body: new URLSearchParams({ language: select.value }),
      });
      if (!response.ok) throw new Error();
      const currentPage = window.location.pathname + window.location.search + window.location.hash;
      window.location.replace("/workspace-language?return=" + encodeURIComponent(currentPage));
    } catch {
      select.value = savedLanguage;
      select.disabled = false;
      window.alert(select.dataset.error);
    }
  });
  readLanguage();
}
