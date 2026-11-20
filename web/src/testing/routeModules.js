// Existing page tests render modules directly. Lazy loading has its own tests.
export function eagerRouteModules() {
  const names = ['Worklists', 'Playbooks', 'Readiness', 'WorkPlan', 'Evidence', 'History', 'FollowUpReport', 'WorkflowDialogContent', 'ApiEvidence', 'ApiReadiness'];
  return {
    name: 'direct-page-modules', enforce: 'pre',
    load(id) {
      if (id.endsWith('/src/lazyPages.js')) return names.map(name => 'import ' + name + ' from \'/src/' + name + '.jsx\'; ' + name + '.preload = () => Promise.resolve(); export {' + name + '};').join('\n') + '\nexport function forgetFailedPages() {}';
    },
  };
}

// These extracted views have no hooks. Keep inspecting their real handlers.
export function expandRouteContent(node) {
  if (Array.isArray(node)) return node.map(expandRouteContent);
  if (!node?.props) return node;
  if (['WorkPlan', 'Evidence', 'WorkflowDialogContent'].includes(node.type?.name)) return expandRouteContent(node.type(node.props));
  if (!Object.hasOwn(node.props, 'children')) return node;
  return {...node, props: {...node.props, children: expandRouteContent(node.props.children)}};
}
