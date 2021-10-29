// register service worker

function registerSW(url) {
  navigator.serviceWorker
    .register(url)
    .then(function (reg) {
      reg.onupdatefound = function () {
        const installingWorker = reg.installing;
        if (installingWorker == null) {
          return;
        }
        installingWorker.onstatechange = function () {
          if (installingWorker.state === 'installed') {
            if (navigator.serviceWorker.controller) {
              // At this point, the updated precached content has been fetched,
              // but the previous service worker will still serve the older
              // content until all client tabs are closed.
              console.log(
                'New content is available and will be used when all ' +
                  'tabs for this page are closed'
              );
            } else {
              console.log('Content is cached for offline use.');
            }
          }
          if (installingWorker.state === 'activated') {
	    fetch('/content.json')
	      .then(res => res.json())
	      .then(res => renderGallery(res.keys))
	      .catch(err => console.log('failed to fetch content info', err))
          }
        };
      };
    })
    .catch(function (error) {
      // registration failed
      console.log('Registration failed with ' + error);
    });
}

function checkValidSW(url) {
  // Check if the service worker can be found. If it can't reload the page.
  fetch(url, {
    headers: {'Service-Worker': 'script'},
  })
    .then((response) => {
      // Ensure service worker exists, and that we really are getting a JS file.
      const contentType = response.headers.get('content-type');
      if (
        response.status === 404 ||
        (contentType != null && contentType.indexOf('javascript') === -1)
      ) {
        // No service worker found. Probably a different app. Reload the page.
        navigator.serviceWorker.ready.then((registration) => {
          registration.unregister().then(() => {
            window.location.reload();
          });
        });
      } else {
        // Service worker found. Proceed as normal.
        registerSW(url);
      }
    })
    .catch(() => {
      console.log(
        'No internet connection found. App is running in offline mode.'
      );
    });
}

var imgSection = document.querySelector('section');

/**
 * Renders a list of image elements across the webpage
 *
 * @param {{ name: string, url: string }[]}
 */
function renderGallery(items) {
  // load each set of image, alt text, name and caption
  for (var i = 0; i <= items.length - 1; i++) {
    var myImage = document.createElement('img');
    var myFigure = document.createElement('figure');
    var myCaption = document.createElement('caption');

    myImage.src = items[i].url;
    myImage.setAttribute('alt', items[i].name);
    myCaption.innerHTML = '<strong>' + items[i].name + '</strong>';

    imgSection.appendChild(myFigure);
    myFigure.appendChild(myImage);
    myFigure.appendChild(myCaption);
  }
}

window.onload = function () {
  checkValidSW('/sw.js');
  navigator.serviceWorker.ready.then(() => {
    console.log(
      'This web app is serving content from a Myel client in a service worker'
    );
  });
};
